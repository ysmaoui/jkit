// Package authprune decides which config keys are provably bad, from the
// config and one credential check per key.
package authprune

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ysmaoui/jkit/internal/authcheck"
	"github.com/ysmaoui/jkit/internal/config"
)

const (
	FieldDefault = "default"
	FieldAlias   = "alias"
)

type Removal struct {
	Host   string `json:"host"`
	Reason string `json:"reason"`
}

// Report is a key that stays although something is wrong with it, or a value
// a removal loses.
type Report struct {
	Host       string `json:"host"`
	Reason     string `json:"reason"`
	Suggestion string `json:"suggestion"`
}

// Move hands Field (FieldDefault or FieldAlias) from a removed key to a
// surviving key of the same host.
type Move struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Field string `json:"field"`
}

type Plan struct {
	Removals []Removal `json:"removals"`
	Reports  []Report  `json:"reports"`
	Moves    []Move    `json:"moves"`
}

// Decide plans removals from check results, one Entry per config key. A key
// goes only when a surviving key of the same normalised host, configured for
// the same user, proves working credentials: a key not in normal form whose
// normal twin does, or a 401 key with such a duplicate. Unverified results
// never justify a removal.
func Decide(cfg *config.Config, entries []authcheck.Entry) Plan {
	byHost := map[string]authcheck.Entry{}
	for _, e := range entries {
		byHost[e.Host] = e
	}
	groups := map[string][]string{}
	for _, h := range cfg.HostURLs() {
		n := config.NormalizeHost(h)
		groups[n] = append(groups[n], h)
	}
	norms := make([]string, 0, len(groups))
	for n := range groups {
		norms = append(norms, n)
	}
	sort.Strings(norms)

	plan := Plan{Removals: []Removal{}, Reports: []Report{}, Moves: []Move{}}
	for _, n := range norms {
		g := group{cfg: cfg, norm: n, keys: groups[n], entries: byHost}
		g.decide(&plan)
	}
	return plan
}

type group struct {
	cfg     *config.Config
	norm    string
	keys    []string
	entries map[string]authcheck.Entry
}

func (g group) result(k string) string { return g.entries[k].Result }

// usable reports a key the API client can use as is. The check runs against
// the normalised URL, but the client appends paths to the raw key, so a key
// with a fragment, query or surrounding space fails at runtime even when its
// check is valid. Trailing slashes are harmless.
func (g group) usable(k string) bool {
	return strings.TrimRight(k, "/") == g.norm
}

// proves reports a key whose credentials are shown to work for its own user.
func (g group) proves(k string) bool {
	e := g.entries[k]
	return e.Result == authcheck.ResultValid && g.usable(k) && strings.EqualFold(e.Identity, g.cfg.Hosts[k].User)
}

func (g group) sameUser(a, b string) bool {
	return strings.EqualFold(g.cfg.Hosts[a].User, g.cfg.Hosts[b].User)
}

// justifier returns the key whose working credentials make k redundant, or ""
// with a report on k when a valid key exists but cannot stand in for it.
func (g group) justifier(k string, removed map[string]string) (string, *Report) {
	_, twin := g.cfg.Hosts[g.norm]
	var candidates []string
	if k != g.norm && twin {
		candidates = []string{g.norm}
	} else if g.result(k) == authcheck.ResultInvalid {
		candidates = g.keys
	}
	var blocked *Report
	for _, j := range candidates {
		if j == k || g.result(j) != authcheck.ResultValid {
			continue
		}
		if _, gone := removed[j]; gone {
			continue
		}
		if g.proves(j) && g.sameUser(j, k) {
			return j, nil
		}
		if blocked == nil && g.usable(j) {
			blocked = g.blockedReport(j, k)
		}
	}
	return "", blocked
}

func (g group) blockedReport(j, k string) *Report {
	if !g.sameUser(j, k) {
		return &Report{
			Host:       k,
			Reason:     fmt.Sprintf("twin %s is valid but not as this key's user", j),
			Suggestion: fmt.Sprintf("jkit config remove '%s', if this user's key is not needed", k),
		}
	}
	id := g.entries[j].Identity
	return &Report{
		Host:       k,
		Reason:     fmt.Sprintf("twin %s authenticates as '%s', configured user is '%s'", j, id, g.cfg.Hosts[j].User),
		Suggestion: fmt.Sprintf("jkit auth login --host %s --user '%s'", g.norm, id),
	}
}

func (g group) decide(plan *Plan) {
	if g.norm == "" {
		for _, k := range g.keys {
			plan.Reports = append(plan.Reports, Report{Host: k, Reason: "not a URL", Suggestion: fmt.Sprintf("jkit config remove '%s'", k)})
		}
		return
	}

	// Recomputed until stable so no removal rests on a key that is itself
	// removed.
	removed := map[string]string{}
	heir := map[string]string{}
	blocked := map[string]*Report{}
	for changed := true; changed; {
		changed = false
		for _, k := range g.keys {
			j, why := g.justifier(k, removed)
			_, was := removed[k]
			switch {
			case j != "" && heir[k] != j:
				heir[k] = j
				if k != g.norm && j == g.norm {
					removed[k] = fmt.Sprintf("not normalised; twin %s is valid", j)
				} else {
					removed[k] = fmt.Sprintf("invalid token (401); duplicate %s is valid", j)
				}
				changed = true
			case j == "" && was:
				delete(removed, k)
				delete(heir, k)
				changed = true
			}
			blocked[k] = why
		}
	}

	var survivors []string
	for _, k := range g.keys {
		if _, ok := removed[k]; !ok {
			survivors = append(survivors, k)
		}
	}
	if len(survivors) == 0 {
		// Unreachable while every removal needs a surviving key, but a host
		// must never lose its last key.
		for _, k := range g.keys {
			plan.Reports = append(plan.Reports, Report{Host: k, Reason: "last entry for " + g.norm + ", kept: " + removed[k]})
		}
		return
	}

	for _, k := range g.keys {
		if reason, ok := removed[k]; ok {
			plan.Removals = append(plan.Removals, Removal{Host: k, Reason: reason})
		}
	}
	g.planMoves(removed, heir, plan)

	_, twinRemoved := removed[g.norm]
	_, hasTwin := g.cfg.Hosts[g.norm]
	twinSurvives := hasTwin && !twinRemoved
	survivorProves := false
	for _, k := range survivors {
		if g.proves(k) {
			survivorProves = true
		}
	}
	for _, k := range survivors {
		switch {
		case blocked[k] != nil:
			plan.Reports = append(plan.Reports, *blocked[k])
		case g.result(k) == authcheck.ResultInvalid && !survivorProves:
			plan.Reports = append(plan.Reports, Report{
				Host:       k,
				Reason:     "invalid token, no valid twin",
				Suggestion: "jkit auth login --host " + g.norm,
			})
		case k != g.norm && !twinSurvives:
			plan.Reports = append(plan.Reports, Report{
				Host:       k,
				Reason:     "not normalised, no twin",
				Suggestion: reloginThenRemove(g.norm, k),
			})
		case k != g.norm:
			plan.Reports = append(plan.Reports, Report{
				Host:       k,
				Reason:     fmt.Sprintf("not normalised; twin %s is %s, not proven valid", g.norm, g.result(g.norm)),
				Suggestion: reloginThenRemove(g.norm, k),
			})
		}
	}
}

func reloginThenRemove(norm, key string) string {
	return fmt.Sprintf("jkit auth login --host %s, then jkit config remove '%s'", norm, key)
}

// planMoves hands each removed key's default flag and alias to the key that
// justified its removal, which proves() made usable and valid.
func (g group) planMoves(removed, heir map[string]string, plan *Plan) {
	isDefault := map[string]bool{}
	alias := map[string]string{}
	for _, k := range g.keys {
		isDefault[k] = g.cfg.Hosts[k].Default
		alias[k] = g.cfg.Hosts[k].Alias
	}
	for _, k := range g.keys {
		if _, ok := removed[k]; !ok {
			continue
		}
		to := heir[k]
		hc := g.cfg.Hosts[k]
		if hc.Default && !isDefault[to] {
			plan.Moves = append(plan.Moves, Move{From: k, To: to, Field: FieldDefault})
			isDefault[to] = true
		}
		switch {
		case hc.Alias == "" || hc.Alias == alias[to]:
		case alias[to] == "":
			plan.Moves = append(plan.Moves, Move{From: k, To: to, Field: FieldAlias})
			alias[to] = hc.Alias
		default:
			plan.Reports = append(plan.Reports, Report{
				Host:   k,
				Reason: fmt.Sprintf("alias '%s' dropped; %s keeps alias '%s'", hc.Alias, to, alias[to]),
			})
		}
	}
}

// Apply returns a copy of cfg with the plan's removals and moves applied.
func Apply(cfg *config.Config, plan Plan) *config.Config {
	out := &config.Config{Hosts: make(map[string]*config.HostConfig, len(cfg.Hosts))}
	for k, hc := range cfg.Hosts {
		c := *hc
		out.Hosts[k] = &c
	}
	for _, m := range plan.Moves {
		switch m.Field {
		case FieldDefault:
			out.Hosts[m.To].Default = true
		case FieldAlias:
			out.Hosts[m.To].Alias = cfg.Hosts[m.From].Alias
		}
	}
	for _, r := range plan.Removals {
		delete(out.Hosts, r.Host)
	}
	return out
}
