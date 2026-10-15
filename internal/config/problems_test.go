package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveHostNormalizes(t *testing.T) {
	cfg := &Config{Hosts: map[string]*HostConfig{
		"https://ci.example.com":       {User: "a", Alias: "ci"},
		"https://other.example.com/":   {User: "b"},
		"https://ci.example.com:8443/": {User: "c"},
	}}

	host, hc, err := cfg.ResolveHost("https://ci.example.com/")
	require.NoError(t, err)
	assert.Equal(t, "https://ci.example.com", host)
	assert.Equal(t, "a", hc.User)

	host, _, err = cfg.ResolveHost("https://other.example.com")
	require.NoError(t, err)
	assert.Equal(t, "https://other.example.com/", host, "returns the key as configured")

	host, _, err = cfg.ResolveHost(" https://ci.example.com#frag ")
	require.NoError(t, err)
	assert.Equal(t, "https://ci.example.com", host)

	host, _, err = cfg.ResolveHost("https://ci.example.com:8443/")
	require.NoError(t, err)
	assert.Equal(t, "https://ci.example.com:8443/", host, "exact key wins")
}

func TestResolveHostDuplicateAliasIsDeterministic(t *testing.T) {
	cfg := &Config{Hosts: map[string]*HostConfig{
		"https://b.example.com": {Alias: "x"},
		"https://a.example.com": {Alias: "x"},
	}}
	for range 20 {
		host, _, err := cfg.ResolveHost("x")
		require.NoError(t, err)
		assert.Equal(t, "https://a.example.com", host)
	}
}

func TestKeyProblems(t *testing.T) {
	cfg := &Config{Hosts: map[string]*HostConfig{
		"https://a.example.com":    {Alias: "prod"},
		"https://a.example.com/":   {},
		"https://b.example.com?x":  {},
		"https://c.example.com":    {Alias: "prod"},
		"https://ok.example.com":   {Alias: "ok"},
		" https://d.example.com":   {},
		"https://e.example.com/#f": {},
	}}
	got := cfg.KeyProblems()

	assert.NotContains(t, got, "https://ok.example.com")
	assert.Equal(t, []KeyProblem{
		{Kind: ProblemDuplicateKey, Related: []string{"https://a.example.com/"}},
		{Kind: ProblemDuplicateAlias, Alias: "prod", Related: []string{"https://c.example.com"}},
	}, got["https://a.example.com"])
	assert.Equal(t, []KeyProblem{
		{Kind: ProblemNotNormalized, Related: []string{"https://a.example.com"}},
		{Kind: ProblemDuplicateKey, Related: []string{"https://a.example.com"}},
	}, got["https://a.example.com/"])
	assert.Equal(t, []KeyProblem{{Kind: ProblemNotNormalized, Related: []string{"https://b.example.com"}}}, got["https://b.example.com?x"])
	assert.Equal(t, []KeyProblem{{Kind: ProblemNotNormalized, Related: []string{"https://d.example.com"}}}, got[" https://d.example.com"])
	assert.Equal(t, []KeyProblem{{Kind: ProblemNotNormalized, Related: []string{"https://e.example.com"}}}, got["https://e.example.com/#f"])
	assert.Equal(t, []KeyProblem{{Kind: ProblemDuplicateAlias, Alias: "prod", Related: []string{"https://a.example.com"}}}, got["https://c.example.com"])
}

func TestResolveHostAmbiguousNormalizedMatch(t *testing.T) {
	cfg := &Config{Hosts: map[string]*HostConfig{
		"https://h.example.com#x": {User: "a"},
		"https://h.example.com/":  {User: "b"},
	}}
	_, _, err := cfg.ResolveHost("https://h.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https://h.example.com#x, https://h.example.com/")

	host, _, err := cfg.ResolveHost("https://h.example.com/")
	require.NoError(t, err)
	assert.Equal(t, "https://h.example.com/", host, "exact key still wins")
}

func TestKeyProblemsMultipleDefaults(t *testing.T) {
	cfg := &Config{Hosts: map[string]*HostConfig{
		"https://a.example.com": {Default: true},
		"https://b.example.com": {Default: true},
		"https://c.example.com": {},
	}}
	got := cfg.KeyProblems()
	assert.Equal(t, []KeyProblem{{Kind: ProblemMultipleDefaults, Related: []string{"https://b.example.com"}}}, got["https://a.example.com"])
	assert.Equal(t, "multiple defaults, also https://a.example.com", got["https://b.example.com"][0].String())
	assert.NotContains(t, got, "https://c.example.com")
}

func TestKeyProblemString(t *testing.T) {
	assert.Equal(t, "not normalised: https://h.example.com",
		KeyProblem{Kind: ProblemNotNormalized, Related: []string{"https://h.example.com"}}.String())
	assert.Equal(t, "duplicate of https://h.example.com",
		KeyProblem{Kind: ProblemDuplicateKey, Related: []string{"https://h.example.com"}}.String())
	assert.Equal(t, "alias 'x' shared with https://y.example.com",
		KeyProblem{Kind: ProblemDuplicateAlias, Alias: "x", Related: []string{"https://y.example.com"}}.String())
}
