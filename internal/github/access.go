// Package github reads the repository state that workflow files cannot tell us
// about: which secrets, variables and environments actually exist.
//
// Every lookup here can legitimately fail because the user's token lacks a
// permission. That is the normal case, not an exception: listing secrets needs
// admin rights on the repository, which the typical user does not have. So each
// result carries its own Access status, and a scope that could not be read is
// reported as UNKNOWN rather than assumed empty. Assuming empty would produce
// "missing secret" findings for secrets that exist, which is the one mistake
// the product cannot afford.
package github

import "sort"

// Access says whether a scope could be read.
type Access int

const (
	// AccessOK means the listing is complete and can be trusted.
	AccessOK Access = iota
	// AccessDenied means the token lacks the permission. GitHub answers 403 or,
	// for organisation scopes, 404. Both mean the same thing to us.
	AccessDenied
	// AccessMissing means the scope does not exist, for example an environment
	// that is referenced by a job but not configured in the repository.
	AccessMissing
	// AccessError means the call failed for another reason: network, rate limit,
	// server error.
	AccessError
	// AccessSkipped means the lookup was not attempted, in offline mode or
	// without a token.
	AccessSkipped
)

// Readable reports whether the listing can be used to conclude that a name is
// absent. Only AccessOK allows that conclusion.
func (a Access) Readable() bool { return a == AccessOK }

func (a Access) String() string {
	switch a {
	case AccessOK:
		return "ok"
	case AccessDenied:
		return "denied"
	case AccessMissing:
		return "missing"
	case AccessError:
		return "error"
	case AccessSkipped:
		return "skipped"
	}
	return "unknown"
}

// NameSet is the set of names in one scope, plus how the listing went.
//
// A NameSet whose Access is not AccessOK is not empty: it is unknown. Callers
// must check Access before concluding anything from Has.
type NameSet struct {
	Access Access
	// Reason explains a non-OK access in words the user can act on, typically
	// naming the missing permission.
	Reason string

	names map[string]bool
}

// NewNameSet builds a readable set, used by the API client and by the
// declarative fallback in the config file.
func NewNameSet(names []string) NameSet {
	s := NameSet{Access: AccessOK, names: map[string]bool{}}
	for _, n := range names {
		s.names[n] = true
	}
	return s
}

// Unavailable builds a set that could not be read.
func Unavailable(a Access, reason string) NameSet {
	return NameSet{Access: a, Reason: reason}
}

// Has reports whether the name is present. It is only meaningful when Access is
// AccessOK.
func (s NameSet) Has(name string) bool { return s.names[name] }

// Len returns the number of known names.
func (s NameSet) Len() int { return len(s.names) }

// Names returns the sorted names, for diagnostics.
func (s NameSet) Names() []string {
	out := make([]string, 0, len(s.names))
	for n := range s.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Merge returns the union of two sets. The result is readable only if both
// inputs were, since a name absent from an unreadable half proves nothing.
func (s NameSet) Merge(o NameSet) NameSet {
	out := NameSet{Access: AccessOK, names: map[string]bool{}}
	for n := range s.names {
		out.names[n] = true
	}
	for n := range o.names {
		out.names[n] = true
	}
	switch {
	case !s.Access.Readable():
		out.Access, out.Reason = s.Access, s.Reason
	case !o.Access.Readable():
		out.Access, out.Reason = o.Access, o.Reason
	}
	return out
}

// Inventory is everything Yumlab knows about a repository's configured secrets
// and variables.
type Inventory struct {
	Owner string
	Repo  string

	RepoSecrets   NameSet
	RepoVariables NameSet

	// OrgSecrets and OrgVariables list the organisation entries that are
	// actually granted to this repository, not every entry in the organisation.
	// An organisation secret that exists but is not shared with this repository
	// is unusable here, so counting it would hide a real problem.
	OrgSecrets   NameSet
	OrgVariables NameSet

	// Environments lists the deployment environments configured on the repo.
	Environments NameSet

	EnvSecrets   map[string]NameSet
	EnvVariables map[string]NameSet
}

// NewInventory returns an inventory with every scope marked as not attempted.
func NewInventory(owner, repo string, reason string) *Inventory {
	skipped := Unavailable(AccessSkipped, reason)
	return &Inventory{
		Owner:         owner,
		Repo:          repo,
		RepoSecrets:   skipped,
		RepoVariables: skipped,
		OrgSecrets:    skipped,
		OrgVariables:  skipped,
		Environments:  skipped,
		EnvSecrets:    map[string]NameSet{},
		EnvVariables:  map[string]NameSet{},
	}
}

// EnvironmentSecrets returns the secrets of one environment. The zero value is
// reported as skipped, never as an empty readable set.
func (inv *Inventory) EnvironmentSecrets(env string) NameSet {
	if s, ok := inv.EnvSecrets[env]; ok {
		return s
	}
	return Unavailable(AccessSkipped, "environment secrets were not read")
}

// EnvironmentVariables returns the variables of one environment.
func (inv *Inventory) EnvironmentVariables(env string) NameSet {
	if s, ok := inv.EnvVariables[env]; ok {
		return s
	}
	return Unavailable(AccessSkipped, "environment variables were not read")
}
