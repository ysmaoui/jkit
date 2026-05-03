package jenkins

import (
	"encoding/json"
	"os"
	"testing"
)

// TestFlattenPGVTreeParityWithBlueOcean verifies that FlattenPGVTree produces
// a []Stage whose leaf-stage FirstParent chain matches Blue Ocean's /nodes/
// output for the same representative build. Synthetic wrapper IDs differ
// between sources and are excluded from the comparison.
func TestFlattenPGVTreeParityWithBlueOcean(t *testing.T) {
	pgvBytes, err := os.ReadFile("testdata/pgv_tree_sample.json")
	if err != nil {
		t.Fatalf("read pgv: %v", err)
	}
	var resp PGVResponse
	if err := json.Unmarshal(pgvBytes, &resp); err != nil {
		t.Fatalf("unmarshal pgv: %v", err)
	}
	flat := FlattenPGVTree(resp.Data.Stages)
	if len(flat) != 178 {
		t.Fatalf("flat count = %d, want 178", len(flat))
	}

	blueBytes, err := os.ReadFile("testdata/blue_nodes_sample.json")
	if err != nil {
		t.Fatalf("read blue: %v", err)
	}
	var blue []Stage
	if err := json.Unmarshal(blueBytes, &blue); err != nil {
		t.Fatalf("unmarshal blue: %v", err)
	}

	blueByID := make(map[string]Stage, len(blue))
	for _, s := range blue {
		blueByID[s.ID] = s
	}

	// Every PGV leaf/branch stage id must exist in Blue Ocean with same
	// FirstParent, except PARALLEL_BLOCK wrappers which have Blue Ocean
	// synthetic ids (`{firstBranchId}-parallel-synthetic`).
	var mismatches int
	for _, p := range flat {
		if p.Type == "PARALLEL_BLOCK" {
			continue
		}
		b, ok := blueByID[p.ID]
		if !ok {
			t.Errorf("pgv stage id=%s not in blue ocean", p.ID)
			continue
		}
		// Blue Ocean branches inside parallel point at the synthetic wrapper;
		// PGV branches point at the PARALLEL_BLOCK id. Compare name parity only.
		if p.Name != b.Name {
			t.Errorf("id=%s name mismatch: pgv=%q blue=%q", p.ID, p.Name, b.Name)
		}
		if p.Type == "STAGE" && p.FirstParent != "" && b.FirstParent != "" {
			// Skip branches (different parent id) and the first node in each
			// parallel branch (its parent is the PARALLEL branch id vs the
			// same id in blue — those should match).
			if p.FirstParent != b.FirstParent && !isParallelWrapper(b.FirstParent) {
				mismatches++
				if mismatches <= 3 {
					t.Logf("parent chain differ id=%s pgv.fp=%s blue.fp=%s", p.ID, p.FirstParent, b.FirstParent)
				}
			}
		}
	}
	if mismatches > 0 {
		t.Errorf("%d STAGE firstParent mismatches vs Blue Ocean", mismatches)
	}
}

func isParallelWrapper(id string) bool {
	return len(id) > len("-parallel-synthetic") && id[len(id)-len("-parallel-synthetic"):] == "-parallel-synthetic"
}

func TestFlattenPGVTreeParallelBranchesPointAtBlock(t *testing.T) {
	tree := []PGVStage{
		{
			ID:   "block1",
			Name: "Parallel",
			Type: "PARALLEL_BLOCK",
			Children: []PGVStage{
				{ID: "b1", Name: "branch-a", Type: "PARALLEL", Children: []PGVStage{
					{ID: "s1", Name: "inner-1", Type: "STAGE"},
					{ID: "s2", Name: "inner-2", Type: "STAGE"},
				}},
				{ID: "b2", Name: "branch-b", Type: "PARALLEL"},
				{ID: "b3", Name: "branch-c", Type: "PARALLEL"},
			},
		},
	}
	flat := FlattenPGVTree(tree)
	byID := map[string]Stage{}
	for _, s := range flat {
		byID[s.ID] = s
	}
	for _, id := range []string{"b1", "b2", "b3"} {
		if byID[id].FirstParent != "block1" {
			t.Errorf("branch %s fp=%q want block1", id, byID[id].FirstParent)
		}
	}
	if byID["s1"].FirstParent != "b1" {
		t.Errorf("s1 fp=%q want b1", byID["s1"].FirstParent)
	}
	if byID["s2"].FirstParent != "s1" {
		t.Errorf("s2 fp=%q want s1 (sequential chaining within branch)", byID["s2"].FirstParent)
	}
	if byID["block1"].FirstParent != "" {
		t.Errorf("block1 fp=%q want empty (root)", byID["block1"].FirstParent)
	}
}

func TestFlattenPGVTreeSequentialSiblingsChain(t *testing.T) {
	tree := []PGVStage{
		{ID: "1", Name: "a", Type: "STAGE", State: "success"},
		{ID: "2", Name: "b", Type: "STAGE", State: "failure"},
		{ID: "3", Name: "c", Type: "STAGE", State: "unstable"},
	}
	flat := FlattenPGVTree(tree)
	if flat[0].FirstParent != "" || flat[1].FirstParent != "1" || flat[2].FirstParent != "2" {
		t.Errorf("unexpected parent chain: %+v", flat)
	}
	if flat[0].Status != "SUCCESS" || flat[1].Status != "FAILURE" || flat[2].Status != "UNSTABLE" {
		t.Errorf("state mapping lost: %+v", flat)
	}
}
