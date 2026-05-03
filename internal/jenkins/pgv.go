package jenkins

// FlattenPGVTree converts a PGV nested stage tree into a flat []Stage with
// FirstParent wired to mimic the Blue Ocean /nodes/ model: sequential siblings
// chain via previous-id, and children of a PARALLEL_BLOCK all point back to
// the block id. Downstream code (NonContainerStages, BuildStageTree) then
// works unchanged.
func FlattenPGVTree(stages []PGVStage) []Stage {
	var out []Stage
	walkPGV(stages, "", "", &out)
	return out
}

func walkPGV(stages []PGVStage, parentID, parentType string, out *[]Stage) {
	// Branches inside a PARALLEL_BLOCK all point back to the block id — no
	// sibling chaining. Everywhere else, siblings chain sequentially to match
	// Blue Ocean's flat-chain model.
	fanOut := parentType == "PARALLEL_BLOCK"
	var prevSiblingID string
	for _, s := range stages {
		var fp string
		if fanOut || prevSiblingID == "" {
			fp = parentID
		} else {
			fp = prevSiblingID
		}
		*out = append(*out, Stage{
			ID:             s.ID,
			Name:           s.Name,
			Status:         MapPGVState(s.State),
			DurationMillis: s.TotalDurationMillis,
			FirstParent:    fp,
			Type:           s.Type,
		})
		if len(s.Children) > 0 {
			walkPGV(s.Children, s.ID, s.Type, out)
		}
		prevSiblingID = s.ID
	}
}

// MapPGVState converts a lowercase PGV state to the uppercase Blue-Ocean-style
// value used by Stage.Status. Unknown inputs return "".
//
// PGV occasionally emits "success" for stages skipped via when{} — that
// ambiguity is not resolvable client-side.
func MapPGVState(s string) string {
	switch s {
	case "success":
		return "SUCCESS"
	case "failure":
		return "FAILURE"
	case "unstable":
		return "UNSTABLE"
	case "aborted":
		return "ABORTED"
	case "not_built", "skipped":
		return "NOT_BUILT"
	case "running":
		return "IN_PROGRESS"
	case "paused":
		return "PAUSED_PENDING_INPUT"
	case "queued":
		return "QUEUED"
	case "unknown", "":
		return ""
	}
	return ""
}
