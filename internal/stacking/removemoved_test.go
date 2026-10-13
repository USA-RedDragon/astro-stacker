package stacking

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func deletingS3(t *testing.T) (*minio.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "not implemented", http.StatusNotImplemented)
			return
		}
		k, _ := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), "/out/"))
		mu.Lock()
		deleted = append(deleted, k)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	s3, err := minio.New(strings.TrimPrefix(srv.URL, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	return s3, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(deleted)
		slices.Sort(out)
		return out
	}
}

func TestRemoveMovedDeletesOnlyOldLayout(t *testing.T) {
	t.Parallel()
	s3, deleted := deletingS3(t)
	p := &Pipeline{s3: s3, dest: "out"}
	ctx := context.Background()
	same := "stacks/M31/Red/master.fit"
	p.removeMoved(ctx, &same, "stacks/M31/Red", "master.fit")
	p.removeMoved(ctx, nil, "stacks/M31/Red", "master.fit")
	old := "stacks/C/2025 A6 (Lemmon)/Green/master.fit"
	p.removeMoved(ctx, &old, "stacks/C-2025 A6 (Lemmon)/Green", "master.fit", "state.fit")
	if want := []string{"stacks/C/2025 A6 (Lemmon)/Green/master.fit", "stacks/C/2025 A6 (Lemmon)/Green/state.fit"}; !slices.Equal(deleted(), want) {
		t.Errorf("deleted %q, want %q", deleted(), want)
	}
}

func TestRemoveOldLayoutKeepsOtherTargetsAndNewKeys(t *testing.T) {
	t.Parallel()
	s3, deleted := deletingS3(t)
	p := &Pipeline{s3: s3, dest: "out"}
	ctx := context.Background()
	p.removeOldLayout(ctx, "M31", []string{"stacks/M31/reference.fit"})
	p.removeOldLayout(ctx, "C/2025 A6 (Lemmon)", []string{
		"stacks/C/2025 A6 (Lemmon)/reference.fit",
		"stacks/C/2025 A6 (Lemmon)/nights/2026-10-12/reference.fit",
		"stacks/C-2025 A6 (Lemmon)/Green/master.fit",
		"stacks/C/2025 R2 (SWAN)/reference.fit",
	})
	want := []string{"stacks/C/2025 A6 (Lemmon)/nights/2026-10-12/reference.fit", "stacks/C/2025 A6 (Lemmon)/reference.fit"}
	if got := deleted(); !slices.Equal(got, want) {
		t.Errorf("deleted %q, want %q", got, want)
	}
}
