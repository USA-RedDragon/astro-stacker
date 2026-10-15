package starcat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	resumeTries = 8
	resumeWait  = 10 * time.Second
)

var errReaderDone = errors.New("the reader is done")

// downloadResuming streams url through fn like download, but when the
// connection breaks or no byte arrives for stallTimeout it asks for the
// rest with a Range request, while the server still serves the same file,
// so a long download does not start over.
func (o *MirrorOptions) downloadResuming(ctx context.Context, url string, counter, total *atomic.Int64, fn func(io.Reader, *http.Response) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	first, err := o.fetch(ctx, url, 0, "")
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = pw.CloseWithError(o.copyResuming(ctx, url, first, pw))
	}()
	cr := &countingReader{r: pr, wait: func(n int) error { return o.Limiter.wait(ctx, n) }, n: counter, total: total, touch: func() {}}
	err = fn(cr, first.resp)
	_ = pr.CloseWithError(errReaderDone)
	cancel()
	<-done
	return err
}

// fetched is one request's response, cancelled when no byte has arrived
// for stallTimeout.
type fetched struct {
	resp   *http.Response
	cancel context.CancelCauseFunc
	cause  func() error
	last   *atomic.Int64
}

func (f *fetched) close() {
	_ = f.resp.Body.Close()
	f.cancel(nil)
}

// fetch asks for url from byte off, the server's If-Range validator
// guarding a resumed request.
func (o *MirrorOptions) fetch(ctx context.Context, url string, off int64, validator string) (*fetched, error) {
	req, cancel := context.WithCancelCause(ctx)
	r, err := http.NewRequestWithContext(req, http.MethodGet, url, nil)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	r.Header.Set("User-Agent", userAgent)
	want := http.StatusOK
	if off > 0 {
		want = http.StatusPartialContent
		r.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
		r.Header.Set("If-Range", validator)
	}
	resp, err := o.client().Do(r)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	if resp.StatusCode != want {
		_ = resp.Body.Close()
		cancel(nil)
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	f := &fetched{resp: resp, cancel: cancel, cause: func() error { return context.Cause(req) }, last: &atomic.Int64{}}
	f.last.Store(time.Now().UnixNano())
	go watchStall(req, cancel, f.last)
	return f, nil
}

func watchStall(ctx context.Context, cancel context.CancelCauseFunc, last *atomic.Int64) {
	t := time.NewTicker(min(10*time.Second, stallTimeout/4))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if time.Since(time.Unix(0, last.Load())) > stallTimeout {
				cancel(fmt.Errorf("no data for %s", stallTimeout))
				return
			}
		}
	}
}

// copyResuming copies the file into w from the first response, then from
// each resumed one, pausing longer after each failure in a row and giving
// up after resumeTries of them, or at once when the server names no
// validator or size, or serves a changed file.
func (o *MirrorOptions) copyResuming(ctx context.Context, url string, f *fetched, w io.Writer) error {
	validator := f.resp.Header.Get("ETag")
	if validator == "" {
		validator = f.resp.Header.Get("Last-Modified")
	}
	size := f.resp.ContentLength
	wait := o.ResumeWait
	if wait <= 0 {
		wait = resumeWait
	}
	var off int64
	failures := 0
	buf := make([]byte, 256<<10)
	for {
		err := copyFetched(f, w, buf, &off, &failures)
		f.close()
		if err == nil && (size < 0 || off == size) {
			return nil
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, errReaderDone) || ctx.Err() != nil || validator == "" || size < 0 {
			return err
		}
		for {
			failures++
			if failures > resumeTries {
				return fmt.Errorf("%s: gave up resuming at byte %d of %d: %w", url, off, size, err)
			}
			slog.Warn("Download broke; resuming", "url", url, "byte", off, "of", size, "try", failures, "error", err)
			select {
			case <-time.After(time.Duration(failures) * wait):
			case <-ctx.Done():
				return err
			}
			var ferr error
			if f, ferr = o.fetch(ctx, url, off, validator); ferr == nil {
				ferr = checkRange(f.resp, off, size)
				if ferr == nil {
					break
				}
				f.close()
			}
			err = ferr
		}
	}
}

func copyFetched(f *fetched, w io.Writer, buf []byte, off *int64, failures *int) error {
	for {
		n, err := f.resp.Body.Read(buf)
		if n > 0 {
			f.last.Store(time.Now().UnixNano())
			*failures = 0
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			*off += int64(n)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if c := f.cause(); c != nil && !errors.Is(c, context.Canceled) {
				return fmt.Errorf("%w (%w)", err, c)
			}
			return err
		}
	}
}

func checkRange(resp *http.Response, off, size int64) error {
	start, total, ok := contentRange(resp.Header.Get("Content-Range"))
	if !ok || start != off || total != size {
		return fmt.Errorf("the rest from byte %d came back as %q of %d bytes", off, resp.Header.Get("Content-Range"), size)
	}
	return nil
}

// contentRange reads "bytes start-end/size".
func contentRange(h string) (start, size int64, ok bool) {
	r, found := strings.CutPrefix(h, "bytes ")
	if !found {
		return 0, 0, false
	}
	span, total, found := strings.Cut(r, "/")
	first, _, found2 := strings.Cut(span, "-")
	if !found || !found2 {
		return 0, 0, false
	}
	start, err1 := strconv.ParseInt(first, 10, 64)
	size, err2 := strconv.ParseInt(total, 10, 64)
	return start, size, err1 == nil && err2 == nil
}
