package jenkins

import (
	"encoding/json"
	"strings"
)

type Job struct {
	Class     string `json:"_class"`
	Name      string `json:"name"`
	FullName  string `json:"fullName"`
	URL       string `json:"url"`
	Color     string `json:"color"`
	LastBuild *Build `json:"lastBuild"`
	InQueue   bool   `json:"inQueue"`
	Jobs      []Job  `json:"jobs,omitempty"`
}

// IsFolder returns true if the job is a folder-type container.
func (j Job) IsFolder() bool {
	return strings.Contains(j.Class, "Folder") || strings.Contains(j.Class, "OrganizationFolder")
}

// IsMultibranch returns true if the job is a multibranch pipeline container.
// Such a job has no builds of its own — only its branch child-jobs do.
func (j Job) IsMultibranch() bool {
	return strings.Contains(j.Class, "MultiBranch")
}

// IsContainer returns true if the job holds child jobs rather than builds
// (a folder or a multibranch pipeline).
func (j Job) IsContainer() bool {
	return j.IsFolder() || j.IsMultibranch()
}

// ParameterDefinition describes a build parameter a job accepts, as returned by
// the job's ParametersDefinitionProperty.
type ParameterDefinition struct {
	Class       string          `json:"_class"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Default     *ParameterValue `json:"defaultParameterValue,omitempty"`
	RawChoices  json.RawMessage `json:"choices,omitempty"`
}

type ParameterValue struct {
	RawValue json.RawMessage `json:"value"`
}

// String renders a default parameter value, handling string/bool/number JSON types.
func (v ParameterValue) String() string {
	if len(v.RawValue) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(v.RawValue, &s); err == nil {
		return s
	}
	return strings.Trim(string(v.RawValue), "\"")
}

// Kind returns a friendly parameter type (e.g. "string", "choice", "boolean",
// "password", "text") derived from the Jenkins class name.
func (p ParameterDefinition) Kind() string {
	t := p.Type
	if t == "" {
		t = p.Class
	}
	if i := strings.LastIndex(t, "."); i >= 0 {
		t = t[i+1:]
	}
	t = strings.TrimSuffix(t, "ParameterDefinition")
	if t == "" {
		return "unknown"
	}
	return strings.ToLower(t)
}

// IsSecret reports whether the parameter holds a secret (password) value.
func (p ParameterDefinition) IsSecret() bool {
	return p.Kind() == "password"
}

// DefaultString returns the default value, masking secrets.
func (p ParameterDefinition) DefaultString() string {
	if p.Default == nil {
		return ""
	}
	val := p.Default.String()
	if val != "" && p.IsSecret() {
		return "••••••"
	}
	return val
}

// Choices returns the list of allowed values for a choice parameter, or nil.
func (p ParameterDefinition) Choices() []string {
	if len(p.RawChoices) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(p.RawChoices, &out); err == nil {
		return out
	}
	return nil
}

// Kind returns a friendly job category derived from the Jenkins class:
// "folder", "multibranch", "pipeline", "freestyle", or a shortened class name.
func (j Job) Kind() string {
	switch {
	case j.IsMultibranch():
		return "multibranch"
	case j.IsFolder():
		return "folder"
	case strings.Contains(j.Class, "WorkflowJob"):
		return "pipeline"
	case strings.Contains(j.Class, "FreeStyleProject"):
		return "freestyle"
	}
	if i := strings.LastIndex(j.Class, "."); i >= 0 {
		return j.Class[i+1:]
	}
	if j.Class == "" {
		return "job"
	}
	return j.Class
}

type Build struct {
	Number     int           `json:"number"`
	Result     string        `json:"result"`
	Building   bool          `json:"building"`
	Duration   int64         `json:"duration"`
	Timestamp  int64         `json:"timestamp"`
	URL        string        `json:"url"`
	Actions    []BuildAction `json:"actions,omitempty"`
	ChangeSets []ChangeSet   `json:"changeSets,omitempty"`
}

// Parameters returns build parameters from the actions list.
func (b Build) Parameters() []BuildParam {
	for _, a := range b.Actions {
		if strings.Contains(a.Class, "ParametersAction") && len(a.Parameters) > 0 {
			return a.Parameters
		}
	}
	return nil
}

// Cause returns the first build trigger description from CauseAction.
func (b Build) Cause() string {
	for _, a := range b.Actions {
		if strings.Contains(a.Class, "CauseAction") && len(a.Causes) > 0 {
			return a.Causes[0].ShortDescription
		}
	}
	return ""
}

type BuildAction struct {
	Class      string       `json:"_class"`
	Parameters []BuildParam `json:"parameters,omitempty"`
	Causes     []BuildCause `json:"causes,omitempty"`
}

type BuildCause struct {
	Class            string `json:"_class"`
	ShortDescription string `json:"shortDescription"`
}

type BuildParam struct {
	Name     string          `json:"name"`
	RawValue json.RawMessage `json:"value"`
}

// Value returns the parameter value as a string, handling bool/number/string JSON types.
func (p BuildParam) Value() string {
	if len(p.RawValue) == 0 {
		return ""
	}
	// Try string first
	var s string
	if err := json.Unmarshal(p.RawValue, &s); err == nil {
		return s
	}
	// Fall back to raw representation (works for bool, number, null)
	return strings.Trim(string(p.RawValue), "\"")
}

type Stage struct {
	ID             string `json:"id"`
	Name           string `json:"displayName"`
	Status         string `json:"result"`
	DurationMillis int64  `json:"durationInMillis"`
	FirstParent    string `json:"firstParent,omitempty"`
	Type           string `json:"type,omitempty"`
}

// PGVResponse is the envelope returned by Pipeline Graph View endpoints.
type PGVResponse struct {
	Status string  `json:"status"`
	Data   PGVData `json:"data"`
}

type PGVData struct {
	Complete bool       `json:"complete"`
	Stages   []PGVStage `json:"stages"`
	Steps    []PGVStep  `json:"steps,omitempty"`
}

// PGVStage is a node in the PGV stage tree. Types: STAGE, PARALLEL, PARALLEL_BLOCK.
type PGVStage struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	State               string     `json:"state"`
	Type                string     `json:"type"`
	Title               string     `json:"title"`
	PauseDurationMillis int64      `json:"pauseDurationMillis"`
	StartTimeMillis     int64      `json:"startTimeMillis"`
	TotalDurationMillis int64      `json:"totalDurationMillis"`
	Children            []PGVStage `json:"children"`
	IsSequential        bool       `json:"isSequential"`
	Synthetic           bool       `json:"synthetic"`
	Placeholder         bool       `json:"placeholder"`
	Agent               string     `json:"agent"`
	URL                 string     `json:"url"`
}

// PGVStep is a single step within a stage (returned by /stages/steps).
type PGVStep struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	State               string `json:"state"`
	Type                string `json:"type"`
	Title               string `json:"title"`
	PauseDurationMillis int64  `json:"pauseDurationMillis"`
	StartTimeMillis     int64  `json:"startTimeMillis"`
	TotalDurationMillis int64  `json:"totalDurationMillis"`
	StageID             string `json:"stageId"`
}

type QueueItem struct {
	ID   int    `json:"id"`
	Why  string `json:"why"`
	Task struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"task"`
	Executable *struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	} `json:"executable"`
}

type LogChunk struct {
	Text    string
	Offset  int64
	HasMore bool
}

type TestReport struct {
	Duration  float64     `json:"duration"`
	FailCount int         `json:"failCount"`
	PassCount int         `json:"passCount"`
	SkipCount int         `json:"skipCount"`
	Suites    []TestSuite `json:"suites"`
}

type TestSuite struct {
	Name  string     `json:"name"`
	Cases []TestCase `json:"cases"`
}

type TestCase struct {
	ClassName    string  `json:"className"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Duration     float64 `json:"duration"`
	ErrorDetails string  `json:"errorDetails"`
}

type ChangeSet struct {
	Items []Change `json:"items"`
}

type Change struct {
	CommitID string `json:"commitId"`
	Message  string `json:"msg"`
	Author   struct {
		FullName string `json:"fullName"`
	} `json:"author"`
	Timestamp int64 `json:"timestamp"`
}

type Artifact struct {
	FileName     string `json:"fileName"`
	RelativePath string `json:"relativePath"`
}
