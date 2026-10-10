package starfront_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const openID = "d07d82eb861e"

type fakeServer struct {
	mu    sync.Mutex
	paths []string
	agent string
	down  bool
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	f.agent = r.UserAgent()
	down := f.down
	f.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusBadGateway)
		return
	}
	var file string
	switch {
	case r.URL.Path == "/api/v1/projects":
		file = "testdata/projects.json"
	case strings.HasPrefix(r.URL.Path, "/api/v1/projects/"):
		file = "testdata/project.json"
	case r.URL.Path == "/api/v1/sky":
		file = "testdata/sky.json"
	default:
		http.NotFound(w, r)
		return
	}
	body, err := os.ReadFile(file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
}

func newPoller(t *testing.T, url string, now time.Time) *starfront.Poller {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.StarfrontCache{}); err != nil {
		t.Fatal(err)
	}
	return &starfront.Poller{BaseURL: url, DB: db, Now: func() time.Time { return now }, Pause: func(time.Duration) {}}
}

func TestOnceFetchesPolitely(t *testing.T) {
	t.Parallel()
	fs := &fakeServer{}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	now := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	p := newPoller(t, srv.URL, now)
	if err := p.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := p.State()
	if len(st.Projects) != 10 || st.FetchedAt == nil || st.Sky == nil || st.Sky.Telescopes != 17 || !st.Enabled {
		t.Fatalf("state %+v", st)
	}
	sum, ok := st.Summaries[openID]
	if !ok || sum.Joined == 0 || sum.Declined == 0 || sum.Reporters == 0 || sum.Hours["H"] <= 0 {
		t.Errorf("summary %+v", sum)
	}
	for _, path := range fs.paths {
		if !strings.HasPrefix(path, "GET ") {
			t.Errorf("wrote to Starfront: %s", path)
		}
	}
	if !strings.Contains(fs.agent, "read-only") {
		t.Errorf("user agent %q", fs.agent)
	}
	first := len(fs.paths)
	if err := p.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again := len(fs.paths) - first; again != 3 {
		t.Errorf("second poll made %d requests, want the list, the one open project and the sky", again)
	}
	var rows []app.StarfrontCache
	p.DB.Find(&rows)
	for _, r := range rows {
		for _, leak := range []string{"RedCat61", "secret-scope", "owner_id", "\"agent\""} {
			if strings.Contains(r.Body, leak) {
				t.Errorf("cache row %s keeps %q", r.Key, leak)
			}
		}
	}
}

func TestLoadFromCacheAndFailure(t *testing.T) {
	t.Parallel()
	fs := &fakeServer{}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	now := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	p := newPoller(t, srv.URL, now)
	if err := p.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	q := &starfront.Poller{BaseURL: srv.URL, DB: p.DB, Now: p.Now, Pause: p.Pause}
	q.Load(context.Background())
	if st := q.State(); len(st.Projects) != 10 || len(st.Summaries) == 0 || st.Sky == nil {
		t.Errorf("loaded state %+v", st)
	}
	fs.mu.Lock()
	fs.down = true
	fs.mu.Unlock()
	if err := q.Once(context.Background()); err == nil {
		t.Error("a failing server reported success")
	}
	if st := q.State(); st.Error == "" || len(st.Projects) != 10 {
		t.Errorf("state after failure %+v", st)
	}
}

func TestRunStops(t *testing.T) {
	t.Parallel()
	fs := &fakeServer{}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	p := newPoller(t, srv.URL, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for p.State().FetchedAt == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if p.State().FetchedAt == nil {
		t.Error("Run never fetched")
	}
}
