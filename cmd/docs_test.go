package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	commandRef = "../docs/commands.md"
	agentSkill = "../agents/skills/jenkins/SKILL.md"
)

// The agent skill is a curated subset, so a few commands are deliberately left
// out of it. Everything else must appear. docs/commands.md has no exemptions.
var skillExemptions = map[string]string{
	"completion": "shell plumbing, nothing for an agent to reason about",
	"config":     "local CLI settings, not a Jenkins operation",
}

// rootFlagNames is snapshotted before any test runs. executeCmd calls
// rootCmd.ResetFlags() and re-registers only the flags it needs, so reading
// PersistentFlags() from inside a test would depend on execution order.
var rootFlagNames []string

func TestMain(m *testing.M) {
	rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if !f.Hidden && f.Name != "help" {
			rootFlagNames = append(rootFlagNames, "--"+f.Name)
		}
	})
	os.Exit(m.Run())
}

func readDoc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestDocsCoverEveryCommand(t *testing.T) {
	docs := map[string]map[string]string{
		commandRef: nil,
		agentSkill: skillExemptions,
	}
	for path, exempt := range docs {
		body := readDoc(t, path)
		for _, c := range rootCmd.Commands() {
			name := c.Name()
			if c.Hidden || name == "help" {
				continue
			}
			if reason, ok := exempt[name]; ok {
				t.Logf("%s: %q exempt — %s", path, name, reason)
				continue
			}
			assert.True(t, strings.Contains(body, "jkit "+name),
				"%s never mentions `jkit %s` — document the command, or exempt it with a reason", path, name)
		}
	}
}

func TestDocsCoverEveryGlobalFlag(t *testing.T) {
	require.NotEmpty(t, rootFlagNames, "flag snapshot is empty — TestMain did not run")
	for _, path := range []string{commandRef, agentSkill} {
		body := readDoc(t, path)
		for _, flag := range rootFlagNames {
			assert.True(t, strings.Contains(body, flag), "%s never mentions %s", path, flag)
		}
	}
}

// Per-command flags were not covered by the global-flag check, so a flag could
// ship undocumented. docs/commands.md is the reference and must name all of
// them; SKILL.md is a curated subset and is not held to this.
func TestCommandReferenceDocumentsEveryLocalFlag(t *testing.T) {
	body := readDoc(t, commandRef)
	for _, c := range rootCmd.Commands() {
		if c.Hidden || c.Name() == "help" {
			continue
		}
		if _, exempt := skillExemptions[c.Name()]; exempt {
			continue
		}
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if f.Hidden || f.Name == "help" {
				return
			}
			assert.True(t, strings.Contains(body, "--"+f.Name),
				"%s never mentions --%s, a local flag of `jkit %s`", commandRef, f.Name, c.Name())
		})
	}
}
