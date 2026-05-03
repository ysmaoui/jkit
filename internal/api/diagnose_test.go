package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiagnoseSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"result":   "SUCCESS",
			"building": false,
			"duration": 120000,
			"url":      "http://jenkins/job/test/42/",
			"actions":  []map[string]any{},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "token")
	result, err := client.Diagnose("test", 42)
	require.NoError(t, err)
	assert.Equal(t, 42, result.Build)
	assert.Equal(t, "SUCCESS", result.Result)
	assert.Equal(t, "2m0s", result.Duration)
	assert.Empty(t, result.FailedStages)
}

func TestDiagnoseFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Build detail
		if path == "/job/test/42/api/json" {
			json.NewEncoder(w).Encode(map[string]any{
				"number":   42,
				"result":   "FAILURE",
				"building": false,
				"duration": 60000,
				"url":      "http://jenkins/job/test/42/",
				"actions": []map[string]any{
					{
						"_class": "hudson.model.CauseAction",
						"causes": []map[string]any{
							{"shortDescription": "Started by user admin", "_class": "hudson.model.Cause$UserIdCause"},
						},
					},
					{
						"_class": "hudson.model.ParametersAction",
						"parameters": []map[string]any{
							{"name": "BRANCH", "value": "main"},
						},
					},
				},
				"changeSets": []map[string]any{
					{
						"items": []map[string]any{
							{"commitId": "abc1234567890", "msg": "Fix stuff", "author": map[string]string{"fullName": "Dev"}},
						},
					},
				},
			})
			return
		}
		// Blue Ocean stages
		if path == "/blue/rest/organizations/jenkins/pipelines/test/runs/42/nodes/" {
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": "10", "displayName": "Build", "result": "SUCCESS", "durationInMillis": 30000},
				{"id": "20", "displayName": "Test", "result": "FAILURE", "durationInMillis": 15000},
			})
			return
		}
		// Stage log for node 20
		if path == "/blue/rest/organizations/jenkins/pipelines/test/runs/42/nodes/20/log/" {
			fmt.Fprint(w, "Running tests...\nERROR: TestFoo failed assertion\njava.lang.AssertionError: expected true\n\tat org.junit.Assert(Assert.java:42)\nTests done\n")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "token")
	result, err := client.Diagnose("test", 42)
	require.NoError(t, err)

	assert.Equal(t, 42, result.Build)
	assert.Equal(t, "FAILURE", result.Result)
	assert.Equal(t, "Started by user admin", result.Cause)
	assert.Equal(t, "main", result.Parameters["BRANCH"])

	require.Len(t, result.FailedStages, 1)
	assert.Equal(t, "Test", result.FailedStages[0].Name)
	assert.NotEmpty(t, result.FailedStages[0].Errors)

	// Should contain the error line
	found := false
	for _, e := range result.FailedStages[0].Errors {
		if e == "ERROR: TestFoo failed assertion" {
			found = true
			break
		}
	}
	assert.True(t, found, "should extract error line from stage log")

	require.Len(t, result.Commits, 1)
	assert.Equal(t, "abc1234", result.Commits[0].Hash)
	assert.Equal(t, "Dev", result.Commits[0].Author)
}

func TestDiagnoseNoStages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/job/test/42/api/json" {
			json.NewEncoder(w).Encode(map[string]any{
				"number":   42,
				"result":   "FAILURE",
				"building": false,
				"duration": 10000,
				"url":      "http://jenkins/job/test/42/",
			})
			return
		}
		// Blue Ocean 404
		if path == "/blue/rest/organizations/jenkins/pipelines/test/runs/42/nodes/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Console log fallback
		if path == "/job/test/42/logText/progressiveText" {
			w.Header().Set("X-Text-Size", "100")
			fmt.Fprint(w, "Building...\nFATAL: compile error in main.go\nBuild step failed\n")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "token")
	result, err := client.Diagnose("test", 42)
	require.NoError(t, err)
	assert.Equal(t, "FAILURE", result.Result)

	// Should fall back to console log
	require.Len(t, result.FailedStages, 1)
	assert.Equal(t, "(console)", result.FailedStages[0].Name)
	found := false
	for _, e := range result.FailedStages[0].Errors {
		if e == "FATAL: compile error in main.go" {
			found = true
		}
	}
	assert.True(t, found, "should find error in console fallback")
}

func TestDiagnoseBuilding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"result":   nil,
			"building": true,
			"duration": 0,
			"url":      "http://jenkins/job/test/42/",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "token")
	result, err := client.Diagnose("test", 42)
	require.NoError(t, err)
	assert.Equal(t, "BUILDING", result.Result)
	assert.Empty(t, result.FailedStages)
}

func TestDiagnoseNestedParallel(t *testing.T) {
	logRequests := make(map[string]bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/job/proj/1/api/json" {
			json.NewEncoder(w).Encode(map[string]any{
				"number": 1, "result": "FAILURE", "building": false,
				"duration": 60000, "url": "http://jenkins/job/proj/1/",
			})
			return
		}
		if path == "/blue/rest/organizations/jenkins/pipelines/proj/runs/1/nodes/" {
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": "1", "displayName": "Build", "result": "SUCCESS", "durationInMillis": 5000},
				// Parallel is a fan-out (2 children) → filtered
				{"id": "2", "displayName": "Parallel", "result": "FAILURE", "durationInMillis": 30000, "firstParent": "1"},
				// branch-a has 1 child (compile) → kept as sequential predecessor
				{"id": "3", "displayName": "branch-a", "result": "FAILURE", "durationInMillis": 20000, "firstParent": "2"},
				{"id": "4", "displayName": "branch-b", "result": "SUCCESS", "durationInMillis": 10000, "firstParent": "2"},
				{"id": "5", "displayName": "compile", "result": "FAILURE", "durationInMillis": 15000, "firstParent": "3"},
			})
			return
		}
		// branch-a (node 3) and compile (node 5) should both be fetched
		if path == "/blue/rest/organizations/jenkins/pipelines/proj/runs/1/nodes/3/log/" {
			logRequests["3"] = true
			fmt.Fprint(w, "ERROR: setup failed\n")
			return
		}
		if path == "/blue/rest/organizations/jenkins/pipelines/proj/runs/1/nodes/5/log/" {
			logRequests["5"] = true
			fmt.Fprint(w, "ERROR: compilation failed\n")
			return
		}
		// Track log requests to fan-out container
		if path == "/blue/rest/organizations/jenkins/pipelines/proj/runs/1/nodes/2/log/" {
			logRequests["2"] = true
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "token")
	result, err := client.Diagnose("proj", 1)
	require.NoError(t, err)

	// Both failed non-container stages should be diagnosed
	require.Len(t, result.FailedStages, 2)
	assert.Equal(t, "branch-a", result.FailedStages[0].Name)
	assert.Equal(t, "compile", result.FailedStages[1].Name)

	// Fan-out container should NOT have its log fetched
	assert.True(t, logRequests["3"], "branch-a log should be fetched")
	assert.True(t, logRequests["5"], "compile log should be fetched")
	assert.False(t, logRequests["2"], "Parallel container log should NOT be fetched")
}

func TestExtractErrors(t *testing.T) {
	tests := []struct {
		name string
		log  string
		want []string
	}{
		{
			name: "error keyword",
			log:  "Starting build\nERROR: something broke\nDone",
			want: []string{"ERROR: something broke"},
		},
		{
			name: "multiple patterns",
			log:  "FATAL: disk full\nWARNING: low space\nUnable to write\n",
			want: []string{"FATAL: disk full", "Unable to write"},
		},
		{
			name: "stack trace",
			log:  "java.lang.NullPointerException\n\tat com.foo.Bar.run(Bar.java:42)\n\tat com.foo.Main.main(Main.java:10)\n",
			want: []string{"java.lang.NullPointerException", "at com.foo.Bar.run(Bar.java:42)", "at com.foo.Main.main(Main.java:10)"},
		},
		{
			name: "no errors",
			log:  "Build started\nCompiling\nDone\n",
			want: nil,
		},
		{
			name: "dedup",
			log:  "ERROR: fail\nERROR: fail\n",
			want: []string{"ERROR: fail"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractErrors(tt.log)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractErrorsCap(t *testing.T) {
	var log string
	for i := 0; i < 50; i++ {
		log += fmt.Sprintf("ERROR: line %d\n", i)
	}
	got := extractErrors(log)
	assert.Len(t, got, 30, "should cap at 30 error lines")
}

func TestFormatAPIDuration(t *testing.T) {
	assert.Equal(t, "< 1s", formatAPIDuration(0))
	assert.Equal(t, "< 1s", formatAPIDuration(500e6))
	assert.Equal(t, "5s", formatAPIDuration(5e9))
	assert.Equal(t, "1m5s", formatAPIDuration(65e9))
	assert.Equal(t, "1h0m", formatAPIDuration(3600e9))
	assert.Equal(t, "1h1m", formatAPIDuration(3661e9))
}
