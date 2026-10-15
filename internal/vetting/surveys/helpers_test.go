package surveys

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type memStore struct {
	mu   sync.Mutex
	objs map[string][]byte
	puts int
}

func newMemStore() *memStore { return &memStore{objs: map[string][]byte{}} }

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, fmt.Errorf("%s: %w", key, ErrNotFound)
	}
	return append([]byte(nil), b...), nil
}

func (m *memStore) Put(_ context.Context, key string, data []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = append([]byte(nil), data...)
	m.puts++
	return nil
}

func (m *memStore) Sizes(_ context.Context, prefix string) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int64{}
	for k, v := range m.objs {
		if strings.HasPrefix(k, prefix) {
			out[segment(prefix, k)] += int64(len(v))
		}
	}
	return out, nil
}

func (m *memStore) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range maps.Keys(m.objs) {
		out = append(out, k)
	}
	return out
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testFetcher(t *testing.T, h http.Handler) (*Fetcher, *memStore) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	st := newMemStore()
	return &Fetcher{
		Store:    st,
		Prefix:   "vetting/cache/",
		HTTP:     srv.Client(),
		IRSAURL:  srv.URL,
		ATLASURL: srv.URL + "/forcedphot",
		WorkDir:  t.TempDir(),
		Now:      func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) },
		atlasGap: -1,
	}, st
}

func ic1396Field() Field {
	return Field{Object: "IC 1396 Panel 1", Slug: "ic-1396-panel-1", RA: 323.277, Dec: 58.6343, Radius: 0.5}
}

func ic1396Star() Target {
	return Target{
		GaiaID: 2179309875368546432, RA: 323.2769976208082, Dec: 58.63428998738852,
		PMRA: -4.049843654863212, PMDec: -3.3442402860664435, G: 14.846538,
		Neighbours: []Neighbour{{GaiaID: 2179309875368546304, RA: 323.27527160984243, Dec: 58.633905889712025, G: 14.498565, Sep: 3.48}},
	}
}
