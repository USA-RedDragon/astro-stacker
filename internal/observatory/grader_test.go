package observatory_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
)

const graderJSON = `{"generated_at":"2026-10-10T06:00:00.000Z","project_id":5,"target_id":13,"target_name":"IC 1318","hfr":{"project_grading":true,"enabled":true,"sigma_factor":4,"accept_improvement":true,"max_sample_size":10,"delay_threshold_percent":0,"mode":"immediate"},"plans":[{"plan_id":7,"filter":"OIII","exposure_seconds":300,"state":"limit","acquired":60,"matching":52,"samples":9,"mean":1.8,"sd":0.07,"lower":1.52,"upper":2.08,"reject_above":2.08,"population_rule":"the 9 newest of 52 subs"}]}`

func TestGraderCacheReportsPluginThenLastKnown(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		calls.Add(1)
		switch r.URL.Query().Get("target_id") {
		case "13":
			_, _ = io.WriteString(w, graderJSON)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"unknown target"}`)
		}
	}))
	defer srv.Close()
	now := time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC)
	m := observatory.NewMonitor(observatory.NewClient(srv.URL, token), nil, nil)
	m.SetStatus(observatory.Status{Version: "5.8.2.203"}, true)
	g := &observatory.GraderCache{Client: observatory.NewClient(srv.URL, token), Monitor: m, Now: func() time.Time { return now }}

	got := g.Limits(context.Background(), []int{13, 13, 99})
	if got.State != observatory.GraderOK || len(got.Targets) != 1 || got.Version != "5.8.2.203" {
		t.Fatalf("%+v", got)
	}
	tg := got.Targets[0]
	p := tg.Report.Plans[0]
	if tg.Source != observatory.GraderFromPlugin || p.Samples != 9 || p.RejectAbove == nil || *p.RejectAbove != 2.08 || tg.Report.HFR.SigmaFactor != 4 {
		t.Fatalf("%+v %+v", tg, p)
	}
	before := calls.Load()
	g.Limits(context.Background(), []int{13})
	if calls.Load() != before {
		t.Fatalf("fresh value refetched")
	}

	m.MarkDown(io.EOF)
	down := g.Limits(context.Background(), []int{13})
	if down.State != observatory.GraderUnreachable || len(down.Targets) != 1 || down.Targets[0].Source != observatory.GraderFromLastKnown || down.Targets[0].Report.Plans[0].Samples != 9 {
		t.Fatalf("%+v", down)
	}
	if none := g.Limits(context.Background(), []int{42}); none.State != observatory.GraderUnreachable || len(none.Targets) != 0 {
		t.Fatalf("%+v", none)
	}
}

func TestGraderCacheOldPluginIsUnsupported(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"not found"}`)
	}))
	defer srv.Close()
	m := observatory.NewMonitor(observatory.NewClient(srv.URL, token), nil, nil)
	m.SetStatus(observatory.Status{Version: "5.8.2.202"}, true)
	g := &observatory.GraderCache{Client: observatory.NewClient(srv.URL, token), Monitor: m}
	got := g.Limits(context.Background(), []int{13})
	if got.State != observatory.GraderUnsupported || got.Note != "grader limit not reported by this plugin version" || got.Version != "5.8.2.202" || len(got.Targets) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestGraderCacheUnconfigured(t *testing.T) {
	t.Parallel()
	g := &observatory.GraderCache{Client: observatory.NewClient("", "")}
	if got := g.Limits(context.Background(), []int{1}); got.State != observatory.GraderUnconfigured {
		t.Fatalf("%+v", got)
	}
}
