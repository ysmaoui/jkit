package output

import (
	"context"
	"fmt"
	"io"
	"time"
)

type FetchLogFunc func(jobPath string, number int, start int64) (text string, offset int64, hasMore bool, err error)

type LogStreamer struct {
	fetchLog     FetchLogFunc
	jobPath      string
	buildNum     int
	writer       io.Writer
	pollInterval time.Duration
}

func NewLogStreamer(fetchLog FetchLogFunc, jobPath string, buildNum int, w io.Writer) *LogStreamer {
	return &LogStreamer{
		fetchLog:     fetchLog,
		jobPath:      jobPath,
		buildNum:     buildNum,
		writer:       w,
		pollInterval: time.Second,
	}
}

func (s *LogStreamer) Stream(ctx context.Context) error {
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		text, newOffset, hasMore, err := s.fetchLog(s.jobPath, s.buildNum, offset)
		if err != nil {
			return fmt.Errorf("streaming log: %w", err)
		}

		if text != "" {
			fmt.Fprint(s.writer, text)
		}

		offset = newOffset
		if !hasMore {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.pollInterval):
		}
	}
}
