package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Jenkins truncates the nested tree query below its requested depth without
// any marker, so a container at the boundary comes back looking empty whether
// or not it has children. An export that silently omits jobs is worse than one
// that costs an extra request.
func TestListJobTreeFollowsFoldersBelowTheQueryDepth(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		folder := func(name, full string, children string) string {
			return `{"_class":"com.cloudbees.hudson.plugins.folder.Folder","name":"` + name +
				`","fullName":"` + full + `","jobs":[` + children + `]}`
		}
		job := func(name, full string) string {
			return `{"_class":"hudson.model.FreeStyleProject","name":"` + name + `","fullName":"` + full + `"}`
		}
		switch r.URL.Path {
		case "/api/json":
			// Five nesting levels; l5's children are cut off by the query depth.
			_, _ = w.Write([]byte(`{"jobs":[` + folder("l1", "l1",
				folder("l2", "l1/l2",
					folder("l3", "l1/l2/l3",
						folder("l4", "l1/l2/l3/l4",
							folder("l5", "l1/l2/l3/l4/l5", ""))))) + `]}`))
		case "/job/l1/job/l2/job/l3/job/l4/job/l5/api/json":
			_, _ = w.Write([]byte(`{"jobs":[` + job("deep", "l1/l2/l3/l4/l5/deep") + `]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "u", "t")

	tree, err := client.ListJobTree("")
	require.NoError(t, err)
	require.Contains(t, asked, "/job/l1/job/l2/job/l3/job/l4/job/l5/api/json",
		"a container at the depth limit must be re-queried, not assumed empty")

	flat := flattenJobs(tree)
	names := make([]string, 0, len(flat))
	for _, j := range flat {
		names = append(names, j.FullName)
	}
	assert.Equal(t, []string{"l1/l2/l3/l4/l5/deep"}, names)
}
