package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// A pipeline inside node{} occupies both a flyweight and a regular executor,
// and the regular one may report the build URL as well. Branch indexing also
// runs on a flyweight executor but is not a build.
const computerJSON = `{"computer":[
 {"oneOffExecutors":[
   {"currentExecutable":{"url":"https://jenkins.example.com/job/team/job/svc/job/main/7/","timestamp":3000}},
   {"currentExecutable":{"url":"https://jenkins.example.com/job/team/job/svc/job/feature%252Fx/2/","timestamp":2000}}
  ],
  "executors":[
   {"currentExecutable":null},
   {"currentExecutable":{"url":"https://jenkins.example.com/job/team/job/svc/job/main/7/","timestamp":3000}}
  ]},
 {"oneOffExecutors":[],
  "executors":[
   {"currentExecutable":{"url":"https://jenkins.example.com/job/team-b/job/app/41/","timestamp":1000}},
   {"currentExecutable":{}},
   {}
  ]},
 {"oneOffExecutors":[
   {"currentExecutable":{"url":"https://jenkins.example.com/job/org/indexing/","timestamp":500}},
   {"currentExecutable":{"url":"https://jenkins.example.com/computer/agent-1/","timestamp":600}},
   {"currentExecutable":{"url":"https://jenkins.example.com/job/late/3/","timestamp":0}}
  ]}
]}`

func TestRunningBuilds(t *testing.T) {
	var gotTree string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/computer/api/json", r.URL.Path)
		gotTree = r.URL.Query().Get("tree")
		_, _ = w.Write([]byte(computerJSON))
	}))
	defer srv.Close()

	builds, err := NewClient(srv.URL, "u", "t").RunningBuilds()
	require.NoError(t, err)
	assert.Equal(t, "computer[oneOffExecutors[currentExecutable[url,timestamp]],executors[currentExecutable[url,timestamp]]]", gotTree)
	assert.Equal(t, []jenkins.RunningBuild{
		{Job: "team-b/app", Number: 41, URL: "https://jenkins.example.com/job/team-b/job/app/41/", Timestamp: 1000},
		{Job: "team/svc/feature%2Fx", Number: 2, URL: "https://jenkins.example.com/job/team/job/svc/job/feature%252Fx/2/", Timestamp: 2000},
		{Job: "team/svc/main", Number: 7, URL: "https://jenkins.example.com/job/team/job/svc/job/main/7/", Timestamp: 3000},
		{Job: "late", Number: 3, URL: "https://jenkins.example.com/job/late/3/"},
	}, builds)
}

func TestRunningBuildsNoneIsEmptySlice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"computer":[{"oneOffExecutors":[],"executors":[{"currentExecutable":null}]}]}`))
	}))
	defer srv.Close()

	builds, err := NewClient(srv.URL, "u", "t").RunningBuilds()
	require.NoError(t, err)
	assert.NotNil(t, builds)
	assert.Empty(t, builds)
}

func TestRunningBuildsForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "u", "t").RunningBuilds()
	var pe *jenkins.PermissionError
	require.True(t, errors.As(err, &pe), "got %v", err)
	assert.Contains(t, err.Error(), "Overall/Read")
}

func TestFilterRunningByFolder(t *testing.T) {
	builds := []jenkins.RunningBuild{
		{Job: "team/svc/main"},
		{Job: "team"},
		{Job: "team-b/app"},
		{Job: "other"},
	}
	jobs := func(bs []jenkins.RunningBuild) []string {
		out := []string{}
		for _, b := range bs {
			out = append(out, b.Job)
		}
		return out
	}
	assert.Equal(t, []string{"team/svc/main", "team"}, jobs(FilterRunningByFolder(builds, "/team/")))
	assert.Equal(t, []string{"team/svc/main"}, jobs(FilterRunningByFolder(builds, "team/svc")))
	assert.Empty(t, FilterRunningByFolder(builds, "tea"))
	assert.Len(t, FilterRunningByFolder(builds, ""), 4)
}
