package jenkins

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The plugin stores the operation as text resolved from its message bundle at
// write time, so a controller running in Japanese writes 変更. Nothing may key
// on the English wording: authorship comes from userID, which is a constant.
func TestConfigChangeIgnoresLocalizedOperation(t *testing.T) {
	system := ConfigChange{Operation: "変更", User: "SYSTEM", UserID: "SYSTEM"}
	human := ConfigChange{Operation: "変更", User: "Ada Lovelace (ada)", UserID: "ada"}

	assert.True(t, system.BySystem())
	assert.False(t, human.BySystem())
	assert.Equal(t, "変更", human.Operation, "the operation is rendered as stored")
}

// Old plugin versions record only the display name.
func TestConfigChangeBySystemFallsBackToUser(t *testing.T) {
	assert.True(t, ConfigChange{User: "SYSTEM"}.BySystem())
	assert.False(t, ConfigChange{User: "ada"}.BySystem())
}

func TestConfigChangeTimestamp(t *testing.T) {
	c := ConfigChange{Date: "2026-08-27_14-58-13"}
	when, ok := c.When()
	assert.True(t, ok)
	assert.Equal(t, 2026, when.Year())
	assert.Equal(t, "2026-08-27 14:58:13", c.Timestamp())

	// An unparseable date is still shown rather than swallowed.
	odd := ConfigChange{Date: "yesterday"}
	_, ok = odd.When()
	assert.False(t, ok)
	assert.Equal(t, "yesterday", odd.Timestamp())
}

func TestConfigChangeWho(t *testing.T) {
	// The display name is self-editable in Jenkins and two people can share
	// one, so the login has to be visible for attribution to mean anything.
	tests := map[string]struct {
		user, userID, want string
	}{
		"both":                {"Ada Lovelace", "ada", "Ada Lovelace (ada)"},
		"login only":          {"", "ada", "ada"},
		"display only":        {"Ada Lovelace", "", "Ada Lovelace"},
		"identical":           {"ada", "ada", "ada"},
		"neither":             {"", "", "not recorded"},
		"display name SYSTEM": {"SYSTEM", "asmith", "SYSTEM (asmith)"},
		"login SYSTEM":        {"Sys Admin", "SYSTEM", "Sys Admin (SYSTEM)"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, ConfigChange{User: tt.user, UserID: tt.userID}.Who())
		})
	}
}

// The plugin percent-encodes names inside the JSON string. Echoing that raw
// shows a branch as "feature%2Fbuild".
func TestDecodeJobName(t *testing.T) {
	assert.Equal(t, "feature/build", DecodeJobName("feature%2Fbuild"))
	assert.Equal(t, "plain-job", DecodeJobName("plain-job"))
	// A stray percent is not an escape; keep the name as it came.
	assert.Equal(t, "100%done", DecodeJobName("100%done"))
}

func TestConfigChangeRename(t *testing.T) {
	c := ConfigChange{Operation: "Renamed", OldName: "feature%2Fold", CurrentName: "feature%2Fnew"}
	assert.Equal(t, "renamed feature/old → feature/new", c.Rename())
	assert.Empty(t, ConfigChange{Operation: "Changed"}.Rename())
}
