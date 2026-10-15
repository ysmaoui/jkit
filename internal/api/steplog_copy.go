package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// CopyStepLog writes one pipeline step's log to w as it stands, with no size
// cap, and returns how many bytes it wrote. A running step is read to its
// last line end and not followed. A step that has written nothing yet copies
// nothing.
//
// The text is what progressiveText sends: a multipart server strips console
// notes, a plain one keeps them. partial means the copy may stop short of the
// last line end: on Jenkins 2.509 to 2.533 a plain answer for a running step
// stops at the 10000-line cap, and the client can only assume the cap was hit.
func (c *Client) CopyStepLog(ctx context.Context, jobPath string, number int, stepID string, w io.Writer) (n int64, partial bool, err error) {
	cw := &byteCounter{w: w}
	log := c.stepLog(jobPath, number, stepID)
	for {
		more, err := log.Read(ctx, cw)
		var nfe *jenkins.NotFoundError
		switch {
		case errors.As(err, &nfe) && cw.n == 0:
			ok, ferr := c.flowNodeExists(ctx, jobPath, number, stepID)
			if ferr != nil {
				return 0, false, fmt.Errorf("reading step %s log: %w", stepID, ferr)
			}
			if !ok {
				return 0, false, fmt.Errorf("reading step %s log: %w", stepID, err)
			}
			return 0, false, nil
		case err != nil:
			return cw.n, false, err
		}
		// Only a counted plain answer stopped by the line cap leaves more to
		// read; elsewhere the cap stalls the read, or the answer reached the
		// last line end.
		if !more || log.CaughtUp() || log.Stalled() {
			return cw.n, log.Stalled(), nil
		}
	}
}

// StepExceptionText returns the error text PGV records for a failed step,
// "" for a step without one. A 404 names what is missing; see explainPGV404.
func (c *Client) StepExceptionText(ctx context.Context, jobPath string, number int, stepID string) (string, error) {
	text, err := c.readExceptionText(ctx, jobPath, number, stepID)
	var nfe *jenkins.NotFoundError
	if errors.As(err, &nfe) {
		return "", c.explainPGV404(ctx, jobPath, number, "stages/exceptionText", err)
	}
	return text, err
}

func (c *Client) readExceptionText(ctx context.Context, jobPath string, number int, stepID string) (string, error) {
	path := fmt.Sprintf("%s/%d/stages/exceptionText", NormalizeJobPath(jobPath), number)
	resp, err := c.getContext(ctx, path, url.Values{"nodeId": {stepID}}, nil)
	if err != nil {
		return "", fmt.Errorf("getting step %s exception text: %w", stepID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	text, err := io.ReadAll(io.LimitReader(resp.Body, maxExceptionText))
	if err != nil {
		return "", fmt.Errorf("reading step %s exception text: %w", stepID, err)
	}
	return string(text), nil
}

type byteCounter struct {
	w io.Writer
	n int64
}

func (c *byteCounter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
