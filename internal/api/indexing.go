package api

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// ScanTarget is a container whose branch indexing can be read, resolved to the
// endpoint that carries its log.
type ScanTarget struct {
	JobPath string
	Kind    string // "multibranch pipeline" or "organization folder"

	// logBase is the computation's URL prefix. A multibranch project publishes
	// it as /indexing; an organization folder is a plain ComputedFolder and
	// publishes it as /computation. There is no api/json under either: the
	// console text is the whole record.
	logBase string
}

// ScanTarget resolves jobPath to the container whose indexing log to read, or
// says why that target has none. Only a multibranch project and an organization
// folder index anything; a plain folder holds jobs, a branch child is a product
// of indexing rather than the thing that runs it, and an ordinary job builds one
// configured source.
func (c *Client) ScanTarget(jobPath string) (*ScanTarget, error) {
	job, err := c.inspectContainer(jobPath)
	if err != nil {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return nil, fmt.Errorf("no job %q on %s — run 'jkit list' to see what is there", jobPath, c.host)
		}
		return nil, err
	}

	switch {
	case strings.Contains(job.Class, "OrganizationFolder"):
		return &ScanTarget{
			JobPath: jobPath,
			Kind:    "organization folder",
			logBase: NormalizeJobPath(jobPath) + "/computation",
		}, nil
	case job.IsMultibranch():
		return &ScanTarget{
			JobPath: jobPath,
			Kind:    "multibranch pipeline",
			logBase: NormalizeJobPath(jobPath) + "/indexing",
		}, nil
	case job.IsFolder():
		return nil, folderScanError(jobPath, *job)
	}

	if parent, branch, ok := c.multibranchParent(jobPath); ok {
		return nil, fmt.Errorf("%q is a branch of %s, and a branch job is created by indexing rather than running it.\n"+
			"Read the scan on the parent:  jkit scan %s --branch %s", jobPath, parent, parent, branch)
	}
	return nil, fmt.Errorf("%q is a %s job: it builds one configured source and never indexes branches, so there is no scan to read.\n"+
		"jkit scan applies to multibranch pipelines and organization folders. For what this job builds:  jkit inspect %s",
		jobPath, job.Kind(), jobPath)
}

// folderScanError names the multibranch jobs inside a folder, because a folder
// is the most common wrong target: it is what the user's path prefix points at.
func folderScanError(jobPath string, job jenkins.Job) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%q is a folder: it holds jobs but indexes nothing itself.\n", jobPath)
	var indexing []string
	for _, child := range job.Jobs {
		if child.IsMultibranch() || strings.Contains(child.Class, "OrganizationFolder") {
			indexing = append(indexing, jobPath+"/"+child.Name)
		}
	}
	if len(indexing) == 0 {
		fmt.Fprintf(&b, "No multibranch job directly inside it — run 'jkit list --folder %s' to look further down", jobPath)
		return errors.New(b.String())
	}
	b.WriteString("Multibranch jobs inside it:")
	for _, name := range indexing {
		b.WriteString("\n  jkit scan " + name)
	}
	return errors.New(b.String())
}

// multibranchParent reports whether jobPath is a branch child, by asking what
// its containing job is. jkit inspect resolves the same relationship from
// config.xml; this route needs no extra permission.
func (c *Client) multibranchParent(jobPath string) (parent, branch string, ok bool) {
	trimmed := strings.Trim(jobPath, "/")
	i := strings.LastIndex(trimmed, "/")
	if i < 0 {
		return "", "", false
	}
	parent, branch = trimmed[:i], trimmed[i+1:]
	job, err := c.inspectContainer(parent)
	if err != nil || !job.IsMultibranch() {
		return "", "", false
	}
	// A branch name with a slash is one job segment, written %2F in the path.
	return parent, strings.ReplaceAll(branch, "%2F", "/"), true
}

// GetScanLog fetches a chunk of the indexing log from byte offset start. Offset
// and HasMore behave as they do for a build console, so the same streamer drives
// a scan that is still running.
func (c *Client) GetScanLog(t *ScanTarget, start int64) (*jenkins.LogChunk, error) {
	chunk, err := c.progressiveChunk(t.logBase+"/logText/progressiveText", start)
	if err != nil {
		return nil, c.scanLogError(t, err)
	}
	return chunk, nil
}

// GetScanLogSize returns the byte size of the indexing log without downloading
// it.
func (c *Client) GetScanLogSize(t *ScanTarget) (int64, error) {
	size, err := c.progressiveSize(t.logBase + "/logText/progressiveText")
	if err != nil {
		return 0, c.scanLogError(t, err)
	}
	return size, nil
}

// scanLogError separates the three ways reading the log can fail once the target
// is known to be a container: no rights, no scan has ever run, or the server
// does not publish the computation where this tool looks. Reporting any of them
// as "job not found" would send the reader after the wrong thing.
func (c *Client) scanLogError(t *ScanTarget, err error) error {
	var nfe *jenkins.NotFoundError
	if errors.As(err, &nfe) {
		return fmt.Errorf("%s is a %s but has no indexing log at %s — it has most likely never been scanned (Jenkins keeps only the last run), or this Jenkins publishes the computation somewhere else",
			t.JobPath, t.Kind, t.logBase)
	}
	var perr *jenkins.PermissionError
	if errors.As(err, &perr) {
		return fmt.Errorf("reading the indexing log of %s needs the Job/Read permission on that job — %s answered HTTP 403 for %s",
			t.JobPath, c.host, t.logBase)
	}
	return err
}
