package api

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// TimestampFormat selects which clock the timestamper plugin prefixes onto each
// console line.
type TimestampFormat string

const (
	// TimestampWallClock prefixes the time of day the line was logged.
	TimestampWallClock TimestampFormat = "time"
	// TimestampElapsed prefixes the time since the build started.
	TimestampElapsed TimestampFormat = "elapsed"
)

// Format patterns handed to the plugin. Neither contains a space, which is what
// lets StampSeparator find the boundary between prefix and log text.
const (
	wallClockPattern = "HH:mm:ss"
	elapsedPattern   = "HH:mm:ss.S"
	// elapsedMillisPattern yields raw elapsed milliseconds, zero-padded to five
	// digits. Longer values are not clamped, so a multi-hour build still reports
	// its true offset.
	elapsedMillisPattern = "SSSSS"
)

// StampSeparator sits between the timestamper prefix and the original log line.
const StampSeparator = "  "

func (f TimestampFormat) pattern() string {
	if f == TimestampWallClock {
		return wallClockPattern
	}
	return elapsedPattern
}

// timestampsQuery builds the parameter set for one /timestamps/ request. The
// plugin picks the clock from which parameter is present, so the format name is
// also the parameter key. startLine is 1-based and may be negative to count back
// from the end of the log; endLine <= 0 means "read to the end".
func timestampsQuery(format TimestampFormat, pattern string, startLine, endLine int, appendLog bool) url.Values {
	q := url.Values{string(format): {pattern}}
	if startLine != 0 {
		q.Set("startLine", strconv.Itoa(startLine))
	}
	if endLine > 0 {
		q.Set("endLine", strconv.Itoa(endLine))
	}
	if appendLog {
		// Presence is what counts; the plugin never reads the value.
		q.Set("appendLog", "")
	}
	return q
}

func timestampsPath(jobPath string, number int) string {
	return fmt.Sprintf("%s/%d/timestamps/", NormalizeJobPath(jobPath), number)
}

// explainTimestampsNotFound turns a 404 on /timestamps/ into an error that names
// which of the two possible causes it hit. The endpoint 404s both when the
// timestamper plugin is absent and when the build does not exist, so the build
// is probed to tell them apart.
func (c *Client) explainTimestampsNotFound(jobPath string, number int, err error) error {
	var nfe *jenkins.NotFoundError
	if !errors.As(err, &nfe) {
		return err
	}
	if _, buildErr := c.GetBuild(jobPath, number); buildErr != nil {
		// The build is the real problem; saying "/timestamps/ not found" would
		// point at the wrong object.
		if hint := c.ContainerHint(jobPath); hint != nil {
			return hint
		}
		var buildNFE *jenkins.NotFoundError
		if errors.As(buildErr, &buildNFE) {
			return fmt.Errorf("no build %s #%d on %s", jobPath, number, c.host)
		}
		return buildErr
	}
	return fmt.Errorf("build %s #%d exists but has no timestamps — the timestamper plugin is not installed on %s",
		jobPath, number, c.host)
}

// StampedLogLines streams the console log of a build with a timestamper prefix,
// calling fn once per PHYSICAL line until it returns false.
//
// Physical, not logical: a console line that carried carriage-return fragments
// (a curl progress meter, say) is stored with embedded newlines, and only its
// first fragment gets a timestamp prefix. Such a line arrives as several calls
// to fn. Counting those calls is therefore NOT a way to count log lines — pass
// the bound to the server as endLine instead.
//
// This endpoint is LINE-indexed, not byte-indexed, so it deliberately does not
// reuse GetBuildLog's byte-offset paging: feeding a byte offset back in as a
// line number silently skips or repeats output. It also reports neither
// X-More-Data nor X-Text-Size, so there is no way to tell from the response
// whether a running build has more to come.
//
// startLine is 1-based and may be negative to count back from the end of the
// log; endLine <= 0 reads to the end. A window past the end of the log is not an
// error — the server answers 200 with an empty body.
func (c *Client) StampedLogLines(jobPath string, number int, format TimestampFormat, startLine, endLine int, fn func(line string) bool) error {
	q := timestampsQuery(format, format.pattern(), startLine, endLine, true)
	resp, err := c.Get(timestampsPath(jobPath, number), q)
	if err != nil {
		return c.explainTimestampsNotFound(jobPath, number, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// bufio.Reader rather than bufio.Scanner: a Scanner token is capped at 64 KB
	// and a build log line can exceed that, which would truncate without saying so.
	r := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			if !fn(strings.TrimRight(line, "\r\n")) {
				return nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("reading timestamped log: %w", err)
		}
	}
}

// BuildLineTimes returns the elapsed milliseconds since build start for every
// console line, without the log text. It is the cheap half of the timestamper
// endpoint: on a 281k-line build it answers in ~2 MB where the console itself is
// ~35 MB, which is what makes gap analysis affordable.
func (c *Client) BuildLineTimes(jobPath string, number int) ([]int64, error) {
	q := timestampsQuery(TimestampElapsed, elapsedMillisPattern, 0, 0, false)
	resp, err := c.Get(timestampsPath(jobPath, number), q)
	if err != nil {
		return nil, c.explainTimestampsNotFound(jobPath, number, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var times []int64
	r := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, readErr := r.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			ms, parseErr := strconv.ParseInt(trimmed, 10, 64)
			if parseErr != nil {
				return nil, fmt.Errorf("parsing line timestamp %q: %w", trimmed, parseErr)
			}
			times = append(times, ms)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return times, nil
			}
			return nil, fmt.Errorf("reading line timestamps: %w", readErr)
		}
	}
}

// StampedLogWindow returns every physical line the endpoint emits for one
// 1-based line window, with elapsed-time prefixes. Used to fetch only the lines
// a gap analysis actually selected. The result can be longer than the window:
// see StampedLogLines on why one log line may render as several.
func (c *Client) StampedLogWindow(jobPath string, number, startLine, endLine int) ([]string, error) {
	var out []string
	err := c.StampedLogLines(jobPath, number, TimestampElapsed, startLine, endLine, func(l string) bool {
		out = append(out, l)
		return true
	})
	return out, err
}
