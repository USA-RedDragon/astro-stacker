//go:build vettingdev

package surveys

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func profilePeaks(path string) func() {
	done := make(chan struct{})
	go func() {
		var best uint64
		for {
			select {
			case <-done:
				return
			case <-time.After(50 * time.Millisecond):
			}
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > best+best/20 {
				best = ms.HeapInuse
				f, err := os.Create(path)
				if err == nil {
					_ = pprof.Lookup("heap").WriteTo(f, 0)
					f.Close()
				}
			}
		}
	}()
	return func() { close(done) }
}

type hashStore struct {
	mu   sync.Mutex
	sums map[string]string
	size map[string]int
}

func (h *hashStore) Get(_ context.Context, key string) ([]byte, error) {
	return nil, fmt.Errorf("%s: %w", key, ErrNotFound)
}

func (h *hashStore) Put(_ context.Context, key string, data []byte, _ string) error {
	s := sha256.Sum256(data)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sums[key] = hex.EncodeToString(s[:])
	h.size[key] = len(data)
	return nil
}

func (h *hashStore) Sizes(context.Context, string) (map[string]int64, error) {
	return map[string]int64{}, nil
}

func vmHWM(t *testing.T) string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "VmHWM:"))
		}
	}
	return "?"
}

// TestDevZTFPull pulls one real ZTF light curve file, ZTF_DEV_FILE,
// cut to the field ZTF_DEV_FIELD ("ra,dec,radius"), and reports the
// process's peak memory and a hash of every tile written.
func TestDevZTFPull(t *testing.T) { //nolint:paralleltest
	path := os.Getenv("ZTF_DEV_FILE")
	parts := strings.Split(os.Getenv("ZTF_DEV_FIELD"), ",")
	if path == "" || len(parts) != 3 {
		t.Skip("ZTF_DEV_FILE and ZTF_DEV_FIELD not set")
	}
	var v [3]float64
	for i, p := range parts {
		x, err := strconv.ParseFloat(p, 64)
		if err != nil {
			t.Fatal(err)
		}
		v[i] = x
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, path) }))
	defer srv.Close()
	st := &hashStore{sums: map[string]string{}, size: map[string]int{}}
	f := &Fetcher{Store: st, Prefix: "dev/", HTTP: srv.Client(), WorkDir: filepath.Dir(path)}
	stem := strings.TrimSuffix(filepath.Base(path), ".parquet")
	var q ZTFQuadrant
	q.Filter = strings.Split(stem, "_")[2]
	field := Field{Slug: "dev", RA: v[0], Dec: v[1], Radius: v[2]}
	if p := os.Getenv("ZTF_DEV_PROF"); p != "" {
		defer profilePeaks(p)()
	}
	e, err := f.pullZTFFile(t.Context(), field, "dr24", srv.URL+"/"+filepath.Base(path), q)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(st.sums))
	for k := range st.sums {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	all := sha256.New()
	biggest := 0
	for _, k := range keys {
		all.Write([]byte(k + st.sums[k]))
		biggest = max(biggest, st.size[k])
	}
	t.Logf("objects %d epochs %d tiles %d bytes %d biggest tile %d", e.Objects, e.Epochs, len(e.Tiles), e.Bytes, biggest)
	t.Logf("tiles hash %s", hex.EncodeToString(all.Sum(nil)))
	t.Logf("peak RSS %s", vmHWM(t))
}
