package authprune

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ysmaoui/jkit/internal/authcheck"
	"github.com/ysmaoui/jkit/internal/config"
)

const h = "https://ci.example.com"

type key struct {
	host, result, alias string
	isDefault           bool
	// user defaults to "u"; identity, the whoAmI name, to user when valid.
	user, identity string
}

func decide(keys ...key) (Plan, *config.Config) {
	cfg := &config.Config{Hosts: map[string]*config.HostConfig{}}
	var entries []authcheck.Entry
	for _, k := range keys {
		if k.user == "" {
			k.user = "u"
		}
		if k.identity == "" && k.result == authcheck.ResultValid {
			k.identity = k.user
		}
		cfg.Hosts[k.host] = &config.HostConfig{User: k.user, Token: "t", Alias: k.alias, Default: k.isDefault}
		entries = append(entries, authcheck.Entry{Host: k.host, Result: k.result, Identity: k.identity})
	}
	return Decide(cfg, entries), cfg
}

func removedHosts(p Plan) []string {
	out := []string{}
	for _, r := range p.Removals {
		out = append(out, r.Host)
	}
	return out
}

func TestMalformedWithValidTwinRemoved(t *testing.T) {
	for _, res := range []string{authcheck.ResultValid, authcheck.ResultInvalid, authcheck.ResultUnreachable} {
		p, _ := decide(key{host: h, result: authcheck.ResultValid}, key{host: h + "#", result: res})
		assert.Equal(t, []Removal{{Host: h + "#", Reason: "not normalised; twin " + h + " is valid"}}, p.Removals, res)
		assert.Empty(t, p.Reports, res)
	}
}

func TestInvalidDuplicateOfValidRemoved(t *testing.T) {
	p, _ := decide(key{host: h, result: authcheck.ResultInvalid}, key{host: h + "/", result: authcheck.ResultValid})
	assert.Equal(t, []Removal{{Host: h, Reason: "invalid token (401); duplicate " + h + "/ is valid"}}, p.Removals)
}

// The check runs against the normalised URL, so a valid fragment or query key
// still fails at runtime and must not stand in for anything.
func TestUnusableValidKeyJustifiesNothing(t *testing.T) {
	for _, suffix := range []string{"#", "?x=1", " "} {
		p, _ := decide(key{host: h, result: authcheck.ResultInvalid, isDefault: true}, key{host: h + suffix, result: authcheck.ResultValid})
		assert.Empty(t, p.Removals, suffix)
		assert.Empty(t, p.Moves, suffix)
		assert.Contains(t, p.Reports, Report{Host: h, Reason: "invalid token, no valid twin", Suggestion: "jkit auth login --host " + h}, suffix)
	}
}

func TestStaleNormalKeyHandsOverToUsableTwin(t *testing.T) {
	p, cfg := decide(
		key{host: h, result: authcheck.ResultInvalid, isDefault: true, alias: "prod"},
		key{host: h + "/", result: authcheck.ResultValid},
		key{host: h + "#", result: authcheck.ResultValid, alias: "other"},
	)
	assert.Equal(t, []string{h}, removedHosts(p))
	assert.Equal(t, []Move{{From: h, To: h + "/", Field: FieldDefault}, {From: h, To: h + "/", Field: FieldAlias}}, p.Moves)
	out := Apply(cfg, p)
	assert.True(t, out.Hosts[h+"/"].Default)
	assert.Equal(t, "prod", out.Hosts[h+"/"].Alias)
	assert.False(t, out.Hosts[h+"#"].Default)
}

func TestOtherUsersKeyKept(t *testing.T) {
	t.Run("configured user differs", func(t *testing.T) {
		p, _ := decide(key{host: h, result: authcheck.ResultValid, user: "alice"}, key{host: h + "/", result: authcheck.ResultValid, user: "bob"})
		assert.Empty(t, p.Removals)
		assert.Equal(t, []Report{{Host: h + "/", Reason: "twin " + h + " is valid but not as this key's user",
			Suggestion: "jkit config remove '" + h + "/', if this user's key is not needed"}}, p.Reports)
	})
	t.Run("twin authenticates as someone else", func(t *testing.T) {
		p, _ := decide(
			key{host: h, result: authcheck.ResultValid, user: "admin@corp", identity: "admin"},
			key{host: h + "#", result: authcheck.ResultInvalid, user: "admin@corp"},
		)
		assert.Empty(t, p.Removals)
		assert.Equal(t, []Report{{Host: h + "#", Reason: "twin " + h + " authenticates as 'admin', configured user is 'admin@corp'",
			Suggestion: "jkit auth login --host " + h + " --user 'admin'"}}, p.Reports)
	})
	t.Run("user case ignored", func(t *testing.T) {
		p, _ := decide(key{host: h, result: authcheck.ResultValid, user: "Alice"}, key{host: h + "#", result: authcheck.ResultInvalid, user: "alice"})
		assert.Equal(t, []string{h + "#"}, removedHosts(p))
	})
}

func TestNotAURL(t *testing.T) {
	p, _ := decide(key{host: "/", result: authcheck.ResultUnreachable}, key{host: "#", result: authcheck.ResultUnreachable})
	assert.Empty(t, p.Removals)
	assert.Equal(t, []Report{
		{Host: "#", Reason: "not a URL", Suggestion: "jkit config remove '#'"},
		{Host: "/", Reason: "not a URL", Suggestion: "jkit config remove '/'"},
	}, p.Reports)
}

func TestNeverRemoved(t *testing.T) {
	for _, res := range []string{authcheck.ResultUnreachable, authcheck.ResultTimeout, "http_500", "http_302",
		authcheck.ResultForbidden, authcheck.ResultAnonymous} {
		t.Run("twin "+res, func(t *testing.T) {
			p, _ := decide(key{host: h, result: res}, key{host: h + "#", result: authcheck.ResultValid})
			assert.Empty(t, p.Removals)
			assert.Equal(t, []string{h + "#"}, hostsOf(p.Reports))
		})
		// h/ is usable, valid and the same user, so only rule 2's 401 test
		// keeps these keys.
		t.Run("normal key "+res+" next to valid duplicate", func(t *testing.T) {
			p, _ := decide(key{host: h, result: res}, key{host: h + "/", result: authcheck.ResultValid})
			assert.Empty(t, p.Removals)
		})
		t.Run("twinless non-normal key "+res+" next to valid duplicate", func(t *testing.T) {
			p, _ := decide(key{host: h + "#", result: res}, key{host: h + "/", result: authcheck.ResultValid})
			assert.Empty(t, p.Removals)
		})
		t.Run("other host "+res, func(t *testing.T) {
			p, _ := decide(key{host: h, result: authcheck.ResultValid}, key{host: "https://qa.example.com", result: res})
			assert.Empty(t, p.Removals)
			assert.Empty(t, p.Reports)
		})
	}
	t.Run("401 without valid twin", func(t *testing.T) {
		p, _ := decide(key{host: h, result: authcheck.ResultInvalid}, key{host: h + "#", result: authcheck.ResultTimeout})
		assert.Empty(t, p.Removals)
		assert.Contains(t, p.Reports, Report{Host: h, Reason: "invalid token, no valid twin", Suggestion: "jkit auth login --host " + h})
	})
	t.Run("only entry", func(t *testing.T) {
		for _, res := range []string{authcheck.ResultInvalid, authcheck.ResultValid} {
			p, _ := decide(key{host: h + "/", result: res})
			assert.Empty(t, p.Removals)
			assert.Len(t, p.Reports, 1)
		}
	})
}

func hostsOf(rs []Report) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.Host)
	}
	return out
}

func TestDefaultAndAliasMove(t *testing.T) {
	p, cfg := decide(
		key{host: h, result: authcheck.ResultValid},
		key{host: h + "#", result: authcheck.ResultInvalid, alias: "prod", isDefault: true},
	)
	assert.Equal(t, []string{h + "#"}, removedHosts(p))
	assert.Equal(t, []Move{{From: h + "#", To: h, Field: FieldDefault}, {From: h + "#", To: h, Field: FieldAlias}}, p.Moves)

	out := Apply(cfg, p)
	assert.Len(t, out.Hosts, 1)
	assert.True(t, out.Hosts[h].Default)
	assert.Equal(t, "prod", out.Hosts[h].Alias)
	assert.Contains(t, cfg.Hosts, h+"#", "Apply must not change its input")
	assert.False(t, cfg.Hosts[h].Default)
}

func TestSurvivorKeepsOwnAlias(t *testing.T) {
	p, cfg := decide(
		key{host: h, result: authcheck.ResultValid, alias: "ci"},
		key{host: h + "/", result: authcheck.ResultValid, alias: "prod"},
	)
	assert.Empty(t, p.Moves)
	assert.Equal(t, []Report{{Host: h + "/", Reason: "alias 'prod' dropped; " + h + " keeps alias 'ci'"}}, p.Reports)
	assert.Equal(t, "ci", Apply(cfg, p).Hosts[h].Alias)
}

func TestHeirPrefersValidNormalKey(t *testing.T) {
	p, _ := decide(
		key{host: h, result: authcheck.ResultValid},
		key{host: h + "/", result: authcheck.ResultValid},
		key{host: h + "#", result: authcheck.ResultInvalid, isDefault: true},
	)
	assert.ElementsMatch(t, []string{h + "/", h + "#"}, removedHosts(p))
	assert.Equal(t, []Move{{From: h + "#", To: h, Field: FieldDefault}}, p.Moves)
}

func TestHeirWithoutNormalKey(t *testing.T) {
	p, _ := decide(
		key{host: h, result: authcheck.ResultInvalid, isDefault: true},
		key{host: h + "/", result: authcheck.ResultValid},
	)
	assert.Equal(t, []Move{{From: h, To: h + "/", Field: FieldDefault}}, p.Moves)
}

func TestTwinRuleBeatsDuplicateRule(t *testing.T) {
	// h// is a 401 key but also non-normal, so it goes in favour of its twin h,
	// as does the valid h/, and the default lands on h.
	p, _ := decide(
		key{host: h, result: authcheck.ResultValid},
		key{host: h + "/", result: authcheck.ResultValid, isDefault: true},
		key{host: h + "//", result: authcheck.ResultInvalid},
	)
	assert.ElementsMatch(t, []string{h + "/", h + "//"}, removedHosts(p))
	assert.Equal(t, []Move{{From: h + "/", To: h, Field: FieldDefault}}, p.Moves)
}

func TestNothingToDo(t *testing.T) {
	p, _ := decide(key{host: h, result: authcheck.ResultValid, isDefault: true})
	assert.Equal(t, Plan{Removals: []Removal{}, Reports: []Report{}, Moves: []Move{}}, p)
}
