package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type stubPreviews struct {
	mu    sync.Mutex
	calls int
	start time.Time
	err   error
	gate  chan struct{}
}

func (s *stubPreviews) Preview(context.Context, *time.Time) (json.RawMessage, error) {
	if s.gate != nil {
		<-s.gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return json.RawMessage(fmt.Sprintf(`{"call":%d,"start":%q}`, s.calls, s.start.Format(time.RFC3339))), nil
}

func (s *stubPreviews) PreviewWith(context.Context, observatory.PreviewRequest) (json.RawMessage, error) {
	return nil, errors.New("unused")
}

func (s *stubPreviews) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type previewClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *previewClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *previewClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixedStamp struct {
	mu    sync.Mutex
	value string
	ok    bool
}

func (f *fixedStamp) get(context.Context) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value, f.ok
}

func (f *fixedStamp) set(v string) {
	f.mu.Lock()
	f.value = v
	f.mu.Unlock()
}

func getCall(t *testing.T, p *server.PreviewCache, start *time.Time) int {
	t.Helper()
	data, err := p.Get(context.Background(), start, false)
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Call int }
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v.Call
}

func TestPreviewCacheKeepsPlanUntilInputsChange(t *testing.T) {
	t.Parallel()
	clock := &previewClock{t: time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)}
	src := &stubPreviews{start: time.Date(2026, 10, 10, 23, 17, 0, 0, time.UTC)}
	stamp := &fixedStamp{value: "a", ok: true}
	p := server.NewPreviewCache(src, clock.now)
	p.Stamp = stamp.get

	if c := getCall(t, p, nil); c != 1 {
		t.Fatalf("first %d", c)
	}
	clock.add(29 * time.Minute)
	if c := getCall(t, p, nil); c != 1 {
		t.Fatalf("before dusk with the same inputs %d", c)
	}
	clock.add(time.Minute)
	if c := getCall(t, p, nil); c != 2 {
		t.Fatalf("before dusk past the ahead TTL %d", c)
	}
	clock.add(time.Hour)
	stamp.set("b")
	if c := getCall(t, p, nil); c != 3 {
		t.Fatalf("changed inputs on an old plan %d", c)
	}
	stamp.set("c")
	clock.add(59 * time.Second)
	if c := getCall(t, p, nil); c != 3 {
		t.Fatalf("changed inputs inside the TTL %d", c)
	}
	clock.add(time.Second)
	if c := getCall(t, p, nil); c != 4 {
		t.Fatalf("changed inputs past the TTL %d", c)
	}
	clock.add(4 * time.Hour)
	if c := getCall(t, p, nil); c != 5 {
		t.Fatalf("plan start passed and stable TTL over %d", c)
	}
	clock.add(4 * time.Minute)
	if c := getCall(t, p, nil); c != 5 {
		t.Fatalf("inside the stable TTL %d", c)
	}
	p.Notice(schedcmd.Record{Status: schedcmd.StatusQueued})
	if c := getCall(t, p, nil); c != 5 {
		t.Fatalf("queued command invalidated %d", c)
	}
	p.Notice(schedcmd.Record{Status: schedcmd.StatusPending})
	if c := getCall(t, p, nil); c != 6 {
		t.Fatalf("pending command did not invalidate %d", c)
	}
	p.Notice(schedcmd.Record{Status: schedcmd.StatusApplied})
	if c := getCall(t, p, nil); c != 7 {
		t.Fatalf("after invalidate %d", c)
	}
	if _, err := p.Get(context.Background(), nil, true); err != nil || src.count() != 8 {
		t.Fatalf("forced: %v %d", err, src.count())
	}
}

func TestPreviewCacheWithoutStampUsesTTL(t *testing.T) {
	t.Parallel()
	clock := &previewClock{t: time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)}
	src := &stubPreviews{start: time.Date(2026, 10, 10, 23, 17, 0, 0, time.UTC)}
	p := server.NewPreviewCache(src, clock.now)
	p.Stamp = func(context.Context) (string, bool) { return "", false }
	getCall(t, p, nil)
	clock.add(59 * time.Second)
	if c := getCall(t, p, nil); c != 1 {
		t.Fatalf("inside TTL %d", c)
	}
	clock.add(time.Second)
	if c := getCall(t, p, nil); c != 2 {
		t.Fatalf("past TTL %d", c)
	}
}

func TestPreviewCacheSharesOneRun(t *testing.T) {
	t.Parallel()
	src := &stubPreviews{gate: make(chan struct{})}
	p := server.NewPreviewCache(src, nil)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := p.Get(context.Background(), nil, false); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(10 * time.Millisecond)
	close(src.gate)
	wg.Wait()
	if n := src.count(); n != 1 {
		t.Fatalf("calls %d", n)
	}
}

func TestPreviewCacheWarm(t *testing.T) {
	t.Parallel()
	clock := &previewClock{t: time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)}
	src := &stubPreviews{start: clock.now()}
	stamp := &fixedStamp{value: "a", ok: true}
	p := server.NewPreviewCache(src, clock.now)
	p.Stamp = stamp.get
	ctx := context.Background()

	p.Warm(ctx)
	if src.count() != 1 {
		t.Fatalf("tonight not warmed: %d", src.count())
	}
	if c := getCall(t, p, nil); c != 1 {
		t.Fatalf("request missed the warm cache %d", c)
	}
	tomorrow := time.Date(2026, 10, 11, 23, 15, 0, 0, time.UTC)
	if c := getCall(t, p, &tomorrow); c != 2 {
		t.Fatalf("tomorrow %d", c)
	}
	clock.add(time.Minute)
	p.Warm(ctx)
	if src.count() != 2 {
		t.Fatalf("refreshed with unchanged inputs: %d", src.count())
	}
	stamp.set("b")
	p.Warm(ctx)
	if src.count() != 4 {
		t.Fatalf("changed inputs not refreshed: %d", src.count())
	}
	stamp.set("c")
	clock.add(30 * time.Second)
	p.Warm(ctx)
	if src.count() != 4 {
		t.Fatalf("refreshed inside the TTL: %d", src.count())
	}
	clock.add(10 * time.Second)
	p.Warm(ctx)
	if src.count() != 6 {
		t.Fatalf("not refreshed ahead of the TTL: %d", src.count())
	}
	p.Invalidate()
	p.Warm(ctx)
	if src.count() != 8 {
		t.Fatalf("invalidate did not refresh both: %d", src.count())
	}
	clock.add(time.Hour + 2*time.Minute)
	p.Warm(ctx)
	if src.count() != 9 {
		t.Fatalf("unwatched tomorrow refreshed: %d", src.count())
	}

	src.mu.Lock()
	src.err = errors.New("offline")
	src.mu.Unlock()
	clock.add(10 * time.Minute)
	p.Warm(ctx)
	clock.add(30 * time.Second)
	p.Warm(ctx)
	if src.count() != 10 {
		t.Fatalf("retried before backoff: %d", src.count())
	}
	clock.add(time.Minute)
	p.Warm(ctx)
	if src.count() != 11 {
		t.Fatalf("no retry after backoff: %d", src.count())
	}
}

func TestPreviewCacheRunStopsWithContext(t *testing.T) {
	t.Parallel()
	src := &stubPreviews{}
	p := server.NewPreviewCache(src, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	p.Invalidate()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestSyncStamp(t *testing.T) {
	t.Parallel()
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := server.SyncStamp(nil, nil)(context.Background()); ok {
		t.Fatal("nil db stamped")
	}
	stamp := server.SyncStamp(db, func() string { return "skips" })
	if _, ok := stamp(context.Background()); ok {
		t.Fatal("stamped without sync tables")
	}
	for _, q := range []string{
		`create table sym_incoming_batch (batch_id integer, channel_id text, status text)`,
		`create table sym_outgoing_batch (batch_id integer, channel_id text, status text)`,
		`insert into sym_incoming_batch values (7,'default','OK'),(9,'heartbeat','OK'),(8,'goal','ER')`,
		`insert into sym_outgoing_batch values (3,'goal','OK'),(4,'goal','NE')`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	if s, ok := stamp(context.Background()); !ok || s != "7/3/skips" {
		t.Fatalf("stamp %q %v", s, ok)
	}
	if err := db.Exec(`insert into sym_outgoing_batch values (5,'goal','OK')`).Error; err != nil {
		t.Fatal(err)
	}
	if s, _ := stamp(context.Background()); s != "7/5/skips" {
		t.Fatalf("stamp after a delivered batch %q", s)
	}
}

type versionedPreviews struct {
	mu      sync.Mutex
	version int
	started chan struct{}
	gate    chan struct{}
}

func (s *versionedPreviews) Preview(context.Context, *time.Time) (json.RawMessage, error) {
	s.mu.Lock()
	v := s.version
	s.mu.Unlock()
	if s.started != nil {
		s.started <- struct{}{}
	}
	if s.gate != nil {
		<-s.gate
	}
	return json.RawMessage(fmt.Sprintf(`{"version":%d}`, v)), nil
}

func (s *versionedPreviews) PreviewWith(context.Context, observatory.PreviewRequest) (json.RawMessage, error) {
	return nil, errors.New("unused")
}

func TestPreviewCacheDoesNotJoinARunStartedBeforeInvalidate(t *testing.T) {
	t.Parallel()
	src := &versionedPreviews{started: make(chan struct{}, 4), gate: make(chan struct{})}
	p := server.NewPreviewCache(src, nil)
	first := make(chan error, 1)
	go func() {
		_, err := p.Get(context.Background(), nil, false)
		first <- err
	}()
	<-src.started
	src.mu.Lock()
	src.version = 1
	src.mu.Unlock()
	p.Invalidate()
	second := make(chan json.RawMessage, 1)
	go func() {
		data, err := p.Get(context.Background(), nil, false)
		if err != nil {
			t.Error(err)
		}
		second <- data
	}()
	select {
	case <-src.started:
	case <-time.After(time.Second):
	}
	close(src.gate)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	var v struct{ Version int }
	if err := json.Unmarshal(<-second, &v); err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 {
		t.Fatalf("a request after Invalidate got the plan from before it: version %d", v.Version)
	}
	data, err := p.Get(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &v); err != nil || v.Version != 1 {
		t.Fatalf("cached after both runs: %s %v", data, err)
	}
}
