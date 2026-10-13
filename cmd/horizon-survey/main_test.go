package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	hs "github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey"
)

type slowReader struct {
	r     io.Reader
	delay time.Duration
}

func (s slowReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if errors.Is(err, io.EOF) {
		time.Sleep(s.delay)
	}
	return n, err
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, syscall.EIO }

func TestWatchStdin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		in    io.Reader
		grace time.Duration
		want  error
	}{
		{"stop typed", strings.NewReader("hello\nstop\n"), time.Hour, errStdinStop},
		{"input closes during the run", slowReader{strings.NewReader(""), 20 * time.Millisecond}, 10 * time.Millisecond, errStdinClosed},
		{"input is empty from the start", strings.NewReader(""), time.Hour, nil},
		{"input cannot be read", errReader{}, 0, nil},
	}
	for _, c := range cases {
		ctx, cancel := context.WithCancelCause(t.Context())
		watchStdin(ctx, cancel, c.in, io.Discard, c.grace)
		got := context.Cause(ctx)
		if c.want == nil {
			if got != nil {
				t.Fatalf("%s: cancelled with %v", c.name, got)
			}
			cancel(nil)
			continue
		}
		if !errors.Is(got, c.want) || !errors.Is(got, hs.ErrStopped) {
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
