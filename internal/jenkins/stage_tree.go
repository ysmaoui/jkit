package jenkins

import "strings"

// NonContainerStages filters out fan-out stages (parallel containers) that have
// multiple children. Sequential predecessors (single child) are kept since they
// are real stages with their own logs. For flat pipelines, all stages returned.
func NonContainerStages(stages []Stage) []Stage {
	// Count children per parent.
	childCount := make(map[string]int)
	for _, s := range stages {
		if s.FirstParent != "" {
			childCount[s.FirstParent]++
		}
	}
	if len(childCount) == 0 {
		return stages
	}
	out := make([]Stage, 0, len(stages))
	for _, s := range stages {
		if childCount[s.ID] <= 1 {
			out = append(out, s)
		}
	}
	return out
}

// QualifiedStagePaths maps each stage ID to a human-readable path that
// disambiguates duplicate stage names, e.g. "RemoteExec/Run Bazel Build".
// Segments are the names of the enclosing parallel-branch heads (outermost
// first) followed by the stage's own name. Stages with no parallel ancestor
// map to their bare name.
//
// FirstParent is overloaded: it links both sequential predecessors and
// hierarchical parents (see pgv.go). Only branch heads — stages whose
// FirstParent is a fan-out node (a node with >1 children) — contribute a path
// segment, so flat sequential pipelines yield bare names while parallel
// branches get a qualifying prefix.
func QualifiedStagePaths(stages []Stage) map[string]string {
	childCount := make(map[string]int, len(stages))
	byID := make(map[string]Stage, len(stages))
	for _, s := range stages {
		byID[s.ID] = s
		if s.FirstParent != "" {
			childCount[s.FirstParent]++
		}
	}
	isFanOut := func(id string) bool { return childCount[id] > 1 }

	paths := make(map[string]string, len(stages))
	for _, s := range stages {
		// Collect enclosing branch-head names, innermost first.
		var branches []string
		for id := s.FirstParent; id != ""; {
			a, ok := byID[id]
			if !ok {
				break
			}
			if isFanOut(a.FirstParent) && a.Name != "" {
				branches = append(branches, a.Name)
			}
			id = a.FirstParent
		}
		// Reverse to outermost first, append own name.
		segs := make([]string, 0, len(branches)+1)
		for i := len(branches) - 1; i >= 0; i-- {
			segs = append(segs, branches[i])
		}
		segs = append(segs, s.Name)
		paths[s.ID] = strings.Join(segs, "/")
	}
	return paths
}

// FlatStage is a Stage with a nesting depth for display purposes.
type FlatStage struct {
	Stage
	Depth int
}

// BuildStageTree converts a flat list of stages (as returned by the Blue Ocean
// API) into a depth-annotated list suitable for indented rendering. Parent-child
// relationships are derived from Stage.FirstParent. If no stage has a parent,
// all stages are returned at depth 0 (backward compatible with older Jenkins).
func BuildStageTree(stages []Stage) []FlatStage {
	if len(stages) == 0 {
		return nil
	}

	ids := make(map[string]struct{}, len(stages))
	for _, s := range stages {
		ids[s.ID] = struct{}{}
	}

	// Group children by parent ID, preserving original order.
	children := make(map[string][]int) // parentID -> indices into stages
	var roots []int
	for i, s := range stages {
		if s.FirstParent == "" {
			roots = append(roots, i)
		} else if _, ok := ids[s.FirstParent]; !ok {
			// Orphan: parent not in list, treat as root
			roots = append(roots, i)
		} else {
			children[s.FirstParent] = append(children[s.FirstParent], i)
		}
	}

	// If nothing had a parent, return flat list at depth 0.
	if len(roots) == len(stages) {
		out := make([]FlatStage, len(stages))
		for i, s := range stages {
			out[i] = FlatStage{Stage: s, Depth: 0}
		}
		return out
	}

	// DFS walk emitting each node with its depth.
	// Blue Ocean chains ALL stages via firstParent — sequential and parallel.
	// We indent at two points:
	//   1. Fan-out (multiple children, i.e. parallel) → children at depth+1
	//   2. Branch header (direct child of fan-out) → its successor at depth+1
	// After that, single-child chains stay flat (same depth).
	out := make([]FlatStage, 0, len(stages))
	var walk func(idx, depth int, isBranchHead bool)
	walk = func(idx, depth int, isBranchHead bool) {
		out = append(out, FlatStage{Stage: stages[idx], Depth: depth})
		kids := children[stages[idx].ID]
		if len(kids) > 1 {
			for _, ci := range kids {
				walk(ci, depth+1, true)
			}
		} else if len(kids) == 1 {
			if isBranchHead {
				walk(kids[0], depth+1, false)
			} else {
				walk(kids[0], depth, false)
			}
		}
	}
	for _, ri := range roots {
		walk(ri, 0, false)
	}
	return out
}
