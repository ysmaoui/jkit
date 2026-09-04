package jenkins

import (
	"net/url"
	"time"
)

// systemUserID is Jenkins' ACL.SYSTEM_USERNAME, recorded for every write no
// human made — multibranch re-indexing above all. It is a constant, which is
// why it, and never Operation, decides how an entry is treated.
const systemUserID = "SYSTEM"

// configDateLayout matches the plugin's history directory names. The timestamp
// carries no zone: it is the controller's local time.
const configDateLayout = "2006-01-02_15-04-05"

// ConfigChange is one entry of a job's JobConfigHistory: a single write to the
// job's configuration, who made it and when.
type ConfigChange struct {
	Date string `json:"date"`
	// Operation is display text the plugin resolved from its message bundle at
	// write time and stored, so a controller running in another locale writes
	// e.g. "変更" instead of "Changed". Render it, never branch on it.
	Operation string `json:"operation"`
	User      string `json:"user"`
	UserID    string `json:"userID"`
	// Job is percent-encoded per path segment inside the JSON string
	// ("team/svc/feature%2Fbuild"), so unescaping it whole would turn a branch
	// name's slash into a folder separator. Not usable as a display name.
	Job         string  `json:"job"`
	OldName     string  `json:"oldName"`
	CurrentName string  `json:"currentName"`
	Comment     *string `json:"changeReasonComment"`
	HasConfig   bool    `json:"hasConfig"`
}

// BySystem reports whether Jenkins itself wrote the entry rather than a person.
func (c ConfigChange) BySystem() bool {
	id := c.UserID
	if id == "" {
		id = c.User
	}
	return id == systemUserID
}

// When parses Date. The second return is false when the plugin wrote a format
// this layout does not cover, in which case Date is still worth showing raw.
func (c ConfigChange) When() (time.Time, bool) {
	t, err := time.Parse(configDateLayout, c.Date)
	return t, err == nil
}

// Timestamp renders Date for reading, falling back to the raw field.
func (c ConfigChange) Timestamp() string {
	if t, ok := c.When(); ok {
		return t.Format("2006-01-02 15:04:05")
	}
	return c.Date
}

// Who names the author, preferring the display name over the login.
func (c ConfigChange) Who() string {
	if c.User != "" {
		return c.User
	}
	if c.UserID != "" {
		return c.UserID
	}
	return "unknown"
}

// Rename describes a rename entry, empty for every other operation.
func (c ConfigChange) Rename() string {
	if c.OldName == "" && c.CurrentName == "" {
		return ""
	}
	return "renamed " + DecodeJobName(c.OldName) + " → " + DecodeJobName(c.CurrentName)
}

// Reason returns the comment the author left, if the instance collects one.
func (c ConfigChange) Reason() string {
	if c.Comment == nil {
		return ""
	}
	return *c.Comment
}

// DecodeJobName undoes the percent-encoding the plugin applies to a single job
// name ("feature%2Fbuild"). It is for one name only; a full job path mixes
// encoded names with unencoded folder separators.
func DecodeJobName(name string) string {
	decoded, err := url.PathUnescape(name)
	if err != nil {
		return name
	}
	return decoded
}
