package context

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseJenkinsURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		wantHost    string
		wantJob     string
		wantBuild   int
		wantErr     bool
		errContains string
	}{
		{
			name:     "classic job URL no build",
			url:      "https://jenkins.prod.com/job/team/job/svc/",
			wantHost: "https://jenkins.prod.com",
			wantJob:  "team/svc",
		},
		{
			name:      "classic build URL",
			url:       "https://jenkins.prod.com/job/team/job/svc/42/",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "team/svc",
			wantBuild: 42,
		},
		{
			name:      "console URL",
			url:       "https://jenkins.prod.com/job/team/job/svc/42/console",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "team/svc",
			wantBuild: 42,
		},
		{
			name:      "blue ocean build URL",
			url:       "https://jenkins.prod.com/blue/organizations/jenkins/team%2Fsvc/detail/team%2Fsvc/42/pipeline",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "team/svc",
			wantBuild: 42,
		},
		{
			name:     "blue ocean no build",
			url:      "https://jenkins.prod.com/blue/organizations/jenkins/team%2Fsvc/activity",
			wantHost: "https://jenkins.prod.com",
			wantJob:  "team/svc",
		},
		{
			name:      "blue ocean multibranch pipeline",
			url:       "https://jenkins.prod.com/blue/organizations/jenkins/PIPELINE_X%2FHera2.0/detail/develop/414/pipeline",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "PIPELINE_X/Hera2.0/develop",
			wantBuild: 414,
		},
		{
			name:      "blue ocean multibranch with slash in branch",
			url:       "https://jenkins.prod.com/blue/organizations/jenkins/PIPELINE_X_TESTS%2FHera2.0/detail/E3ASWF-16917%2Frefactoring-static-buildnode/12/pipeline/97/",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "PIPELINE_X_TESTS/Hera2.0/E3ASWF-16917%2Frefactoring-static-buildnode",
			wantBuild: 12,
		},
		{
			name:      "blue ocean deeply nested pipeline with last-component detail",
			url:       "https://jenkins.example.com/blue/organizations/jenkins/E3SP_SDK%2FSystem_Builds%2Ftemp%2F1773223360477%2Fpkgs%2Fflashperm/detail/flashperm/1/pipeline/11338/",
			wantHost:  "https://jenkins.example.com",
			wantJob:   "E3SP_SDK/System_Builds/temp/1773223360477/pkgs/flashperm",
			wantBuild: 1,
		},
		{
			name:      "blue ocean nested pipeline without node ID",
			url:       "https://jenkins.example.com/blue/organizations/jenkins/org%2Fteam%2Fsvc/detail/svc/7/pipeline",
			wantHost:  "https://jenkins.example.com",
			wantJob:   "org/team/svc",
			wantBuild: 7,
		},
		{
			name:      "blue ocean nested multibranch with branch != last component",
			url:       "https://jenkins.example.com/blue/organizations/jenkins/org%2Fteam%2Fsvc/detail/main/3/pipeline",
			wantHost:  "https://jenkins.example.com",
			wantJob:   "org/team/svc/main",
			wantBuild: 3,
		},
		{
			name:      "URL-encoded segments",
			url:       "https://jenkins.prod.com/job/my%20folder/job/my%20job/42/",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "my folder/my job",
			wantBuild: 42,
		},
		{
			name:      "single job with build",
			url:       "https://jenkins.prod.com/job/my-app/42/",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "my-app",
			wantBuild: 42,
		},
		{
			name:      "with port",
			url:       "https://jenkins.prod.com:8080/job/my-app/42/",
			wantHost:  "https://jenkins.prod.com:8080",
			wantJob:   "my-app",
			wantBuild: 42,
		},
		{
			name:      "deeply nested",
			url:       "https://jenkins.prod.com/job/org/job/team/job/svc/42/",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "org/team/svc",
			wantBuild: 42,
		},

		// Branch names with slashes (encoded as %2F or double-encoded as %252F)
		{
			name:      "branch with double-encoded slash (%252F)",
			url:       "https://jenkins.prod.com/job/PIPELINE/job/Hera2.0/job/E3ASWF-16917%252Frefactoring/12/",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "PIPELINE/Hera2.0/E3ASWF-16917%2Frefactoring",
			wantBuild: 12,
		},
		{
			name:      "branch with single-encoded slash (%2F)",
			url:       "https://jenkins.prod.com/job/PIPELINE/job/Hera2.0/job/E3ASWF-16917%2Frefactoring/12/",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "PIPELINE/Hera2.0/E3ASWF-16917%2Frefactoring",
			wantBuild: 12,
		},
		{
			name:     "branch with encoded slash no build",
			url:      "https://jenkins.prod.com/job/PIPELINE/job/feature%2Fbranch/",
			wantHost: "https://jenkins.prod.com",
			wantJob:  "PIPELINE/feature%2Fbranch",
		},

		// Edge cases
		{
			name:      "no trailing slash",
			url:       "https://jenkins.prod.com/job/team/job/svc/42",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "team/svc",
			wantBuild: 42,
		},
		{
			name:     "classic job no build no trailing slash",
			url:      "https://jenkins.prod.com/job/team/job/svc",
			wantHost: "https://jenkins.prod.com",
			wantJob:  "team/svc",
		},
		{
			name:      "query params ignored",
			url:       "https://jenkins.prod.com/job/team/job/svc/42/?foo=bar",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "team/svc",
			wantBuild: 42,
		},
		{
			name:      "fragment ignored",
			url:       "https://jenkins.prod.com/job/team/job/svc/42/#section",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "team/svc",
			wantBuild: 42,
		},
		{
			name:      "artifact URL",
			url:       "https://jenkins.prod.com/job/team/job/svc/42/artifact/build/out.jar",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "team/svc",
			wantBuild: 42,
		},
		{
			name:      "testReport URL",
			url:       "https://jenkins.prod.com/job/team/job/svc/42/testReport/",
			wantHost:  "https://jenkins.prod.com",
			wantJob:   "team/svc",
			wantBuild: 42,
		},
		{
			name:      "http scheme",
			url:       "http://jenkins.local/job/my-app/1/",
			wantHost:  "http://jenkins.local",
			wantJob:   "my-app",
			wantBuild: 1,
		},

		// Error cases
		{
			name:        "non-http scheme",
			url:         "ftp://jenkins.prod.com/job/team/job/svc/",
			wantErr:     true,
			errContains: "unsupported scheme",
		},
		{
			name:        "no job or blue path",
			url:         "https://jenkins.prod.com/manage",
			wantErr:     true,
			errContains: "not a Jenkins URL",
		},
		{
			name:        "empty string",
			url:         "",
			wantErr:     true,
			errContains: "unsupported scheme",
		},
		{
			name:        "just host",
			url:         "https://jenkins.prod.com/",
			wantErr:     true,
			errContains: "not a Jenkins URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseJenkinsURL(tt.url)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, tt.wantHost, got.Host)
			assert.Equal(t, tt.wantJob, got.JobPath)
			assert.Equal(t, tt.wantBuild, got.BuildNumber)
			assert.True(t, got.IsURL)
		})
	}
}
