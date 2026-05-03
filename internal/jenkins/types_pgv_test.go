package jenkins

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPGVResponseDecodesSamplePayload(t *testing.T) {
	data, err := os.ReadFile("testdata/pgv_tree_e3sp_5.json")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	var resp PGVResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want ok", resp.Status)
	}
	if !resp.Data.Complete {
		t.Errorf("complete = false, want true")
	}
	if len(resp.Data.Stages) != 14 {
		t.Fatalf("top-level stages = %d, want 14", len(resp.Data.Stages))
	}

	var parallelBlock *PGVStage
	for i := range resp.Data.Stages {
		if resp.Data.Stages[i].Type == "PARALLEL_BLOCK" {
			parallelBlock = &resp.Data.Stages[i]
			break
		}
	}
	if parallelBlock == nil {
		t.Fatal("no PARALLEL_BLOCK in top-level stages")
	}
	if len(parallelBlock.Children) != 15 {
		t.Errorf("parallel block children = %d, want 15", len(parallelBlock.Children))
	}
	for _, c := range parallelBlock.Children {
		if c.Type != "PARALLEL" {
			t.Errorf("branch %q type = %q, want PARALLEL", c.Name, c.Type)
		}
	}

	total := countStages(resp.Data.Stages)
	if total != 178 {
		t.Errorf("total stages = %d, want 178", total)
	}
}

func countStages(stages []PGVStage) int {
	n := len(stages)
	for _, s := range stages {
		n += countStages(s.Children)
	}
	return n
}
