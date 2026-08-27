// Package score estimates how many minutes a finding wastes.
//
// This number is what makes a report shareable with a lead, so it is not
// optional on a finding. It is also an estimate, and the way it is computed is
// documented rather than hidden: a user who disagrees with the model should be
// able to see exactly what Yumlab assumed.
//
// Until v0.4 reads real run durations from the API, the model uses a single
// assumed run length. Every constant here is deliberately visible.
package score

// AssumedRunMinutes is how long a pipeline is assumed to run before a
// configuration failure surfaces. It stands in for the real duration, which
// only the runs API can provide.
const AssumedRunMinutes = 8

// GhostSecretMinutes is the cost of one secret or variable that is referenced
// but not defined.
//
// The model: the pipeline runs, fails when the step needs the value, the
// developer fixes it and runs again. That is one wasted run plus one re-run.
// The cost is counted once per distinct missing name because CI reveals them
// one at a time: a second missing secret only shows up after the first is
// fixed, costing another full cycle.
const GhostSecretMinutes = 2 * AssumedRunMinutes

// Total sums the estimated minutes of a set of findings.
func Total(minutes []int) int {
	var sum int
	for _, m := range minutes {
		sum += m
	}
	return sum
}

// FormatMinutes renders a duration the way the report shows it.
func FormatMinutes(m int) string {
	switch {
	case m <= 0:
		return "0 min"
	case m < 60:
		return itoa(m) + " min"
	default:
		h, rem := m/60, m%60
		if rem == 0 {
			return itoa(h) + " h"
		}
		return itoa(h) + " h " + itoa(rem) + " min"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
