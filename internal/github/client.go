package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	gh "github.com/google/go-github/v66/github"
)

const perPage = 100

// Client reads secrets, variables and environments from the GitHub API.
type Client struct {
	api   *gh.Client
	owner string
	repo  string
}

// Options configures the client.
type Options struct {
	// Token is the GitHub token. It is required.
	Token string
	// BaseURL points at a GitHub Enterprise instance. Empty means github.com.
	BaseURL string
	// Timeout bounds every individual API call.
	Timeout time.Duration
}

// New builds a client for owner/repo.
func New(owner, repo string, opts Options) (*Client, error) {
	if opts.Token == "" {
		return nil, errors.New("a GitHub token is required")
	}
	if owner == "" || repo == "" {
		return nil, errors.New("repository owner and name are required")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	api := gh.NewClient(&http.Client{Timeout: timeout}).WithAuthToken(opts.Token)
	if opts.BaseURL != "" {
		var err error
		api, err = api.WithEnterpriseURLs(opts.BaseURL, opts.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("configure GitHub base URL %q: %w", opts.BaseURL, err)
		}
	}

	return &Client{api: api, owner: owner, repo: repo}, nil
}

// Inventory reads every scope Yumlab needs. environments limits the environment
// lookups to those actually referenced by the workflows.
//
// Inventory does not fail when a scope cannot be read: the failure is recorded
// in that scope's Access and the rest of the inventory is still collected. It
// only returns an error when nothing at all could be reached, which means the
// result would be meaningless.
func (c *Client) Inventory(ctx context.Context, environments []string) (*Inventory, error) {
	inv := NewInventory(c.owner, c.repo, "not requested")

	repoInfo, _, err := c.api.Repositories.Get(ctx, c.owner, c.repo)
	if err != nil {
		return nil, fmt.Errorf("read repository %s/%s: %w", c.owner, c.repo, describe(err))
	}

	inv.RepoSecrets = c.listRepoSecrets(ctx)
	inv.RepoVariables = c.listRepoVariables(ctx)
	inv.Environments = c.listEnvironments(ctx)

	// A repository owned by a user belongs to no organization, so the set of
	// organization secrets available to it is empty. That is a conclusive fact,
	// not something we failed to read, and recording it as such is what lets the
	// secrets control reach a verdict on a personal repository at all. Asking
	// the organization endpoints here would answer 404, which is indistinguishable
	// from a missing permission and would turn every reference into UNKNOWN.
	if isUserOwned(repoInfo) {
		inv.OrgSecrets = NewNameSet(nil)
		inv.OrgVariables = NewNameSet(nil)
	} else {
		inv.OrgSecrets = c.listOrgSecrets(ctx)
		inv.OrgVariables = c.listOrgVariables(ctx)
	}

	for _, env := range environments {
		// Reading secrets of an environment that does not exist would return a
		// confusing 404. Skip it: the missing environment is itself the finding.
		if inv.Environments.Access.Readable() && !inv.Environments.Has(env) {
			inv.EnvSecrets[env] = Unavailable(AccessMissing, "environment does not exist")
			inv.EnvVariables[env] = Unavailable(AccessMissing, "environment does not exist")
			continue
		}
		inv.EnvSecrets[env] = c.listEnvSecrets(ctx, int(repoInfo.GetID()), env)
		inv.EnvVariables[env] = c.listEnvVariables(ctx, env)
	}

	return inv, nil
}

// isUserOwned reports whether the repository belongs to a user rather than an
// organization. GitHub reports the owner type as "User" or "Organization"; an
// empty type is treated as an organization, which is the cautious reading since
// it leads to asking rather than assuming.
func isUserOwned(repo *gh.Repository) bool {
	return repo.GetOwner().GetType() == "User"
}

func (c *Client) listRepoSecrets(ctx context.Context) NameSet {
	return collect(ctx, "repository secrets",
		"the token needs the \"Secrets\" repository permission (read), which requires admin access on the repository",
		func(opts *gh.ListOptions) ([]string, *gh.Response, error) {
			s, resp, err := c.api.Actions.ListRepoSecrets(ctx, c.owner, c.repo, opts)
			return secretNames(s), resp, err
		})
}

func (c *Client) listRepoVariables(ctx context.Context) NameSet {
	return collect(ctx, "repository variables",
		"the token needs the \"Variables\" repository permission (read)",
		func(opts *gh.ListOptions) ([]string, *gh.Response, error) {
			v, resp, err := c.api.Actions.ListRepoVariables(ctx, c.owner, c.repo, opts)
			return variableNames(v), resp, err
		})
}

// listOrgSecrets uses the repository-scoped endpoint on purpose. It lists the
// organisation secrets granted to this repository, which is both easier to
// authorise than admin:org and more accurate: an organisation secret that is
// not shared with this repository is unusable in its workflows.
func (c *Client) listOrgSecrets(ctx context.Context) NameSet {
	return collect(ctx, "organization secrets",
		"the token needs the \"Secrets\" repository permission (read); organization secrets not shared with this repository are never visible",
		func(opts *gh.ListOptions) ([]string, *gh.Response, error) {
			s, resp, err := c.api.Actions.ListRepoOrgSecrets(ctx, c.owner, c.repo, opts)
			return secretNames(s), resp, err
		})
}

func (c *Client) listOrgVariables(ctx context.Context) NameSet {
	return collect(ctx, "organization variables",
		"the token needs the \"Variables\" repository permission (read)",
		func(opts *gh.ListOptions) ([]string, *gh.Response, error) {
			v, resp, err := c.api.Actions.ListRepoOrgVariables(ctx, c.owner, c.repo, opts)
			return variableNames(v), resp, err
		})
}

func (c *Client) listEnvironments(ctx context.Context) NameSet {
	return collect(ctx, "environments",
		"the token needs the \"Environments\" repository permission (read)",
		func(opts *gh.ListOptions) ([]string, *gh.Response, error) {
			envs, resp, err := c.api.Repositories.ListEnvironments(ctx, c.owner, c.repo,
				&gh.EnvironmentListOptions{ListOptions: *opts})
			if err != nil {
				return nil, resp, err
			}
			var names []string
			for _, e := range envs.Environments {
				names = append(names, e.GetName())
			}
			return names, resp, nil
		})
}

func (c *Client) listEnvSecrets(ctx context.Context, repoID int, env string) NameSet {
	return collect(ctx, "secrets of environment "+env,
		"the token needs the \"Secrets\" and \"Environments\" repository permissions (read)",
		func(opts *gh.ListOptions) ([]string, *gh.Response, error) {
			s, resp, err := c.api.Actions.ListEnvSecrets(ctx, repoID, env, opts)
			return secretNames(s), resp, err
		})
}

func (c *Client) listEnvVariables(ctx context.Context, env string) NameSet {
	return collect(ctx, "variables of environment "+env,
		"the token needs the \"Variables\" and \"Environments\" repository permissions (read)",
		func(opts *gh.ListOptions) ([]string, *gh.Response, error) {
			v, resp, err := c.api.Actions.ListEnvVariables(ctx, c.owner, c.repo, env, opts)
			return variableNames(v), resp, err
		})
}

func secretNames(s *gh.Secrets) []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.Secrets))
	for _, sec := range s.Secrets {
		names = append(names, sec.Name)
	}
	return names
}

func variableNames(v *gh.ActionsVariables) []string {
	if v == nil {
		return nil
	}
	names := make([]string, 0, len(v.Variables))
	for _, va := range v.Variables {
		names = append(names, va.Name)
	}
	return names
}

// collect pages through a listing and turns any failure into an Access status
// instead of an error, so that one unreadable scope never aborts the scan.
func collect(ctx context.Context, what, permission string, fetch func(*gh.ListOptions) ([]string, *gh.Response, error)) NameSet {
	var names []string
	opts := &gh.ListOptions{PerPage: perPage}

	for {
		page, resp, err := fetch(opts)
		if err != nil {
			return classify(what, permission, err)
		}
		names = append(names, page...)
		if resp == nil || resp.NextPage == 0 {
			return NewNameSet(names)
		}
		opts.Page = resp.NextPage

		if err := ctx.Err(); err != nil {
			return Unavailable(AccessError, fmt.Sprintf("reading %s was interrupted: %v", what, err))
		}
	}
}

// classify maps an API error to an Access status.
//
// GitHub answers 403 when a permission is missing and 404 when the token cannot
// even see that the resource exists, which is what happens for organisation
// scopes. Both are permission problems from the user's point of view, and both
// must lead to UNKNOWN rather than to a finding.
func classify(what, permission string, err error) NameSet {
	var apiErr *gh.ErrorResponse
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		switch apiErr.Response.StatusCode {
		case http.StatusForbidden, http.StatusUnauthorized, http.StatusNotFound:
			return Unavailable(AccessDenied, fmt.Sprintf("cannot read %s: %s", what, permission))
		}
	}
	var rateErr *gh.RateLimitError
	if errors.As(err, &rateErr) {
		return Unavailable(AccessError, fmt.Sprintf("cannot read %s: GitHub rate limit reached", what))
	}
	return Unavailable(AccessError, fmt.Sprintf("cannot read %s: %v", what, describe(err)))
}

// describe strips the noisy parts of a go-github error for terminal output.
func describe(err error) error {
	var apiErr *gh.ErrorResponse
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		msg := strings.TrimSpace(apiErr.Message)
		if msg == "" {
			msg = apiErr.Response.Status
		}
		return fmt.Errorf("%s (HTTP %d)", msg, apiErr.Response.StatusCode)
	}
	return err
}
