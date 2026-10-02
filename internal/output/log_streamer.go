package output

import (
	"context"
	"fmt"
	"io"
	"time"
)

// FetchLogFunc writes the log text that is new since its last call to w and
// reports whether the log may still grow.
type FetchLogFunc func(ctx context.Context, w io.Writer) (more bool, err error)

type LogStreamer struct {
	fetchLog     FetchLogFunc
	writer       io.Writer
	pollInterval time.Duration
}

func NewLogStreamer(fetchLog FetchLogFunc, w io.Writer, pollInterval time.Duration) *LogStreamer {
	return &LogStreamer{
		fetchLog:     fetchLog,
		writer:       w,
		pollInterval: pollInterval,
	}
}

func (s *LogStreamer) Stream(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		more, err := s.fetchLog(ctx, s.writer)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("streaming log: %w", err)
		}
		if !more {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.pollInterval):
		}
	}
}
