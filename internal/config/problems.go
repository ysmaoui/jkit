package config

import (
	"fmt"
	"strings"
)

// ProblemKind names a defect in a config key that needs no network to find.
type ProblemKind string

const (
	ProblemNotNormalized    ProblemKind = "not_normalized"
	ProblemDuplicateKey     ProblemKind = "duplicate_key"
	ProblemDuplicateAlias   ProblemKind = "duplicate_alias"
	ProblemMultipleDefaults ProblemKind = "multiple_defaults"
)

// KeyProblem is one defect of a config key. Related holds the normalised key
// for ProblemNotNormalized, and the other keys involved for a duplicate.
type KeyProblem struct {
	Kind    ProblemKind
	Alias   string
	Related []string
}

func (p KeyProblem) String() string {
	related := strings.Join(p.Related, ", ")
	switch p.Kind {
	case ProblemNotNormalized:
		return "not normalised: " + related
	case ProblemDuplicateKey:
		return "duplicate of " + related
	case ProblemDuplicateAlias:
		return fmt.Sprintf("alias '%s' shared with %s", p.Alias, related)
	case ProblemMultipleDefaults:
		return "multiple defaults, also " + related
	}
	return string(p.Kind)
}

// KeyProblems returns the defects of each key that has any. Keys that
// normalise to one host are separate entries for the same Jenkins, and an
// alias shared by several keys makes an alias lookup ambiguous, as does more
// than one default for the default host.
func (c *Config) KeyProblems() map[string][]KeyProblem {
	hosts := c.HostURLs()
	byNorm := map[string][]string{}
	byAlias := map[string][]string{}
	var defaults []string
	for _, h := range hosts {
		if c.Hosts[h].Default {
			defaults = append(defaults, h)
		}
		n := NormalizeHost(h)
		byNorm[n] = append(byNorm[n], h)
		if a := c.Hosts[h].Alias; a != "" {
			byAlias[a] = append(byAlias[a], h)
		}
	}

	out := map[string][]KeyProblem{}
	for _, h := range hosts {
		n := NormalizeHost(h)
		if n != h {
			out[h] = append(out[h], KeyProblem{Kind: ProblemNotNormalized, Related: []string{n}})
		}
		if others := without(byNorm[n], h); len(others) > 0 {
			out[h] = append(out[h], KeyProblem{Kind: ProblemDuplicateKey, Related: others})
		}
		if a := c.Hosts[h].Alias; a != "" {
			if others := without(byAlias[a], h); len(others) > 0 {
				out[h] = append(out[h], KeyProblem{Kind: ProblemDuplicateAlias, Alias: a, Related: others})
			}
		}
		if c.Hosts[h].Default {
			if others := without(defaults, h); len(others) > 0 {
				out[h] = append(out[h], KeyProblem{Kind: ProblemMultipleDefaults, Related: others})
			}
		}
	}
	return out
}

func without(list []string, s string) []string {
	var out []string
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}
