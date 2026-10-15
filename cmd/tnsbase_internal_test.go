package cmd

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryreport"
	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
	configulator "github.com/USA-RedDragon/configulator/v2"
)

func loadTNSEnv(t *testing.T, env map[string]string) config.Discoveries {
	t.Helper()
	cfg, err := configulator.New(config.ConfigSchema()).
		WithEnvironmentVariables(&configulator.EnvironmentVariableOptions{Separator: "_"}).
		WithEnviron(func(k string) (string, bool) { v, ok := env[k]; return v, ok }).
		LoadWithoutValidation()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg.Discoveries
}

func TestTNSBaseURLDefaultsToProduction(t *testing.T) {
	t.Parallel()
	d := loadTNSEnv(t, map[string]string{})
	if d.TNSBaseURL != "https://www.wis-tns.org" {
		t.Fatalf("default tns-base-url %q", d.TNSBaseURL)
	}
	if got := tnsMirrorClient(d).BaseURL; got != "https://www.wis-tns.org/system/files/tns_public_objects/" {
		t.Errorf("mirror base %q", got)
	}
	if got := tnsReports(d).BaseURL; got != "https://www.wis-tns.org/api" {
		t.Errorf("report base %q", got)
	}
	if got := tnsCatalogs(d).TNSURL; got != "https://www.wis-tns.org/api/get/search" {
		t.Errorf("search URL %q", got)
	}
}

func zippedCSV(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.Create("tns_public_objects.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(f, "2026-10-14 00:00:00\nobjid,name\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestTNSBaseURLUsedByEveryCall(t *testing.T) {
	t.Parallel()
	zipped := zippedCSV(t)
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/sandbox/system/files/tns_public_objects/"):
			_, _ = w.Write(zipped)
		case r.URL.Path == "/sandbox/api/get/search":
			_, _ = io.WriteString(w, `{"id_code":200,"id_message":"OK","data":{"reply":[{"prefix":"AT","objname":"2026zzz"}]}}`)
		case r.URL.Path == "/sandbox/api/set/bulk-report":
			_, _ = io.WriteString(w, `{"id_code":200,"id_message":"OK","data":{"report_id":"77"}}`)
		case r.URL.Path == "/sandbox/api/get/bulk-report-reply":
			_, _ = io.WriteString(w, `{"id_code":200,"id_message":"OK","data":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	base := srv.URL + "/sandbox/"
	d := loadTNSEnv(t, map[string]string{
		"DISCOVERIES_TNS_BASE_URL": base,
		"DISCOVERIES_TNS_KEY":      "k",
		"DISCOVERIES_TNS_BOT_ID":   "42",
		"DISCOVERIES_TNS_BOT_NAME": "bot",
	})
	if d.TNSBaseURL != base {
		t.Fatalf("tns-base-url %q", d.TNSBaseURL)
	}
	ctx := context.Background()

	mc := tnsMirrorClient(d)
	mc.Gap = 1
	if _, err := mc.Fetch(ctx, tnsmirror.FullFile); err != nil {
		t.Fatalf("mirror fetch: %v", err)
	}
	as, err := tnsCatalogs(d).TNS(ctx, 10, 20, 5)
	if err != nil || len(as) != 1 || as[0].ID != "AT 2026zzz" {
		t.Fatalf("search %+v %v", as, err)
	}
	rep := tnsReports(d)
	rep.ReplyWait = 1
	if r, err := rep.Send(ctx, map[string]any{"at_report": map[string]any{}}); err != nil || r.ReportID != "77" {
		t.Fatalf("send %+v %v", r, err)
	}

	mu.Lock()
	got := slices.Clone(paths)
	mu.Unlock()
	want := []string{
		"/sandbox/system/files/tns_public_objects/" + tnsmirror.FullFile,
		"/sandbox/api/get/search",
		"/sandbox/api/set/bulk-report",
		"/sandbox/api/get/bulk-report-reply",
	}
	if !slices.Equal(got, want) {
		t.Errorf("paths %q, want %q", got, want)
	}

	if l := discoveryreport.TNSObjectLink(tnsSite(d), "AT 2026zzz"); l != srv.URL+"/sandbox/object/2026zzz" {
		t.Errorf("object link %q", l)
	}
	if l := discoveryreport.TNSSearchLink(tnsSite(d), 10, 20); !strings.HasPrefix(l, srv.URL+"/sandbox/search?") {
		t.Errorf("search link %q", l)
	}
}
