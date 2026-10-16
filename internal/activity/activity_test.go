package activity_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
)

func TestTaskLifecycle(t *testing.T) {
	t.Parallel()
	reg := activity.NewRegistry()
	task := reg.Task("x", "Thing")
	if reg.Task("x", "Other") != task {
		t.Fatal("Task made a second task for the same kind")
	}
	task.Start("first", 10, "files")
	task.Step(3)
	task.SetQueued(2, "targets")
	it := task.Item()
	if it.State != activity.StateRunning || it.Done != 3 || it.Total != 10 || it.Unit != "files" || it.Current != "first" || it.Queued != 2 {
		t.Fatalf("running item %+v", it)
	}
	task.Finish(errors.New("boom"))
	it = task.Item()
	if it.State != activity.StateFailed || it.LastError != "boom" || it.LastFailed == nil {
		t.Fatalf("failed item %+v", it)
	}
	task.Start("again", 1, "files")
	task.Finish(nil)
	it = task.Item()
	if it.State != activity.StateIdle || it.LastFinished == nil || it.LastError != "" || it.Done != 0 {
		t.Fatalf("finished item %+v", it)
	}
}

func TestNilTaskAndRegistryDoNothing(t *testing.T) {
	t.Parallel()
	var reg *activity.Registry
	task := reg.Task("x", "Thing")
	task.Start("a", 1, "b")
	task.Step(1)
	task.Finish(nil)
	task.SetNext(time.Now())
	if task.Running() {
		t.Fatal("nil task runs")
	}
	if len(reg.Snapshot().Items) != 0 {
		t.Fatal("nil registry has items")
	}
}

func TestSnapshotOrdersBusyFirst(t *testing.T) {
	t.Parallel()
	reg := activity.NewRegistry()
	reg.Task("a", "A")
	reg.Task("b", "B").Start("", 0, "")
	reg.Task("c", "C").Finish(errors.New("x"))
	got := reg.Snapshot().Items
	ids := []string{got[0].ID, got[1].ID, got[2].ID}
	if strings.Join(ids, ",") != "c,b,a" {
		t.Fatalf("order %v", ids)
	}
}

type sink struct {
	mu   sync.Mutex
	msgs []activity.Snapshot
}

func (s *sink) Broadcast(typ string, v any, sticky bool) {
	if typ != activity.EventType || !sticky {
		return
	}
	s.mu.Lock()
	if snap, ok := v.(activity.Snapshot); ok {
		s.msgs = append(s.msgs, snap)
	}
	s.mu.Unlock()
}

func (s *sink) last() (activity.Snapshot, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.msgs) == 0 {
		return activity.Snapshot{}, 0
	}
	return s.msgs[len(s.msgs)-1], len(s.msgs)
}

func TestRunSendsSourcesAndChanges(t *testing.T) {
	t.Parallel()
	reg := activity.NewRegistry()
	var reads atomic.Int32
	reg.AddSource(activity.Source{Name: "s", Every: 50 * time.Millisecond, Read: func(context.Context) ([]activity.Item, error) {
		reads.Add(1)
		return []activity.Item{{Kind: "outside", Label: "Outside", State: activity.StateQueued, Queued: 4}}, nil
	}})
	task := reg.Task("inside", "Inside")
	out := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reg.Run(ctx, out)
	waitFor(t, func() bool {
		s, _ := out.last()
		return len(s.Items) == 2 && s.Items[0].ID == "outside"
	})
	task.Start("now", 2, "steps")
	waitFor(t, func() bool {
		s, _ := out.last()
		return len(s.Items) == 2 && s.Items[0].ID == "inside" && s.Items[0].State == activity.StateRunning
	})
	_, n := out.last()
	time.Sleep(300 * time.Millisecond)
	if _, m := out.last(); m != n {
		t.Fatalf("sent %d unchanged snapshots", m-n)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestNextRun(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 4, 20, 0, 0, time.UTC)
	got, err := activity.NextRun("17 * * * *", nil, now)
	if err != nil || !got.Equal(time.Date(2026, 10, 16, 5, 17, 0, 0, time.UTC)) {
		t.Fatalf("hourly: %v %v", got, err)
	}
	tz := "America/Chicago"
	got, err = activity.NextRun("17 6 3 * *", &tz, now)
	if err != nil || !got.Equal(time.Date(2026, 11, 3, 12, 17, 0, 0, time.UTC)) {
		t.Fatalf("monthly in Chicago: %v %v", got, err)
	}
	if _, err := activity.NextRun("nonsense", nil, now); err == nil {
		t.Fatal("bad schedule parsed")
	}
}

func job(name string, labels map[string]string, owner string, active int, cond string, at time.Time) activity.KubeJob {
	var j activity.KubeJob
	j.Metadata.Name = name
	j.Metadata.Labels = labels
	if owner != "" {
		j.Metadata.OwnerReferences = append(j.Metadata.OwnerReferences, activity.KubeOwner{Kind: "CronJob", Name: owner})
	}
	j.Status.Active = active
	start := at.Add(-time.Hour)
	j.Status.StartTime = &start
	if cond != "" {
		j.Status.Conditions = append(j.Status.Conditions,
			activity.KubeCondition{Type: cond, Status: "True", Reason: "BackoffLimitExceeded", LastTransitionTime: at})
	}
	return j
}

const itemsKey = "items"

func kubeServer(t *testing.T, jobs []activity.KubeJob, crons []activity.KubeCronJob, pods []activity.KubePod) *activity.Kube {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			_ = json.NewEncoder(w).Encode(map[string]any{itemsKey: jobs})
		case strings.HasSuffix(r.URL.Path, "/cronjobs"):
			_ = json.NewEncoder(w).Encode(map[string]any{itemsKey: crons})
		case strings.HasSuffix(r.URL.Path, "/pods"):
			_ = json.NewEncoder(w).Encode(map[string]any{itemsKey: pods})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &activity.Kube{Base: srv.URL, Token: func() (string, error) { return "tok", nil }, HTTP: srv.Client()}
}

func TestJobsReadsRunningFailedAndSchedule(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 4, 20, 0, 0, time.UTC)
	jobs := []activity.KubeJob{
		job("discoveries-m13-archive-c", map[string]string{"app.kubernetes.io/name": "discoveries-backfill"}, "", 6, "", now),
		job("vet-1", nil, "discoveries-vetting-vet", 0, "Complete", now.Add(-3*time.Hour)),
		job("vet-2", nil, "discoveries-vetting-vet", 0, "Failed", now.Add(-time.Hour)),
		job("other", map[string]string{"app.kubernetes.io/name": "something"}, "", 1, "", now),
	}
	var cj activity.KubeCronJob
	cj.Metadata.Name = "discoveries-vetting-vet"
	cj.Spec.Schedule = "17 * * * *"
	var pod activity.KubePod
	var cs activity.KubeContainerStatus
	cs.State.Terminated = &activity.KubeTerminated{Reason: "OOMKilled", ExitCode: 137}
	pod.Status.ContainerStatuses = []activity.KubeContainerStatus{cs}
	k := kubeServer(t, jobs, []activity.KubeCronJob{cj}, []activity.KubePod{pod})
	j := &activity.Jobs{Kube: k, Now: func() time.Time { return now }, Watches: []activity.Watch{
		{Kind: "discoveries", Namespace: "ns", Label: "app.kubernetes.io/name=discoveries-backfill"},
		{Kind: "vetting", Namespace: "ns", CronJob: "discoveries-vetting-vet"},
	}}
	if err := j.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := j.Info("discoveries")
	if !d.Running || d.Workers != 6 || d.NextRun != nil || !strings.Contains(d.Warning, "ran out of memory") {
		t.Fatalf("discoveries %+v", d)
	}
	v := j.Info("vetting")
	if v.Running || v.NextRun == nil || !v.NextRun.Equal(time.Date(2026, 10, 16, 5, 17, 0, 0, time.UTC)) {
		t.Fatalf("vetting %+v", v)
	}
	if v.LastFailed == nil || v.LastError != "failed too many times" {
		t.Fatalf("vetting failure %+v", v)
	}
	it := activity.Item{Kind: "vetting", State: activity.StateQueued}
	v.Apply(&it)
	if it.State != activity.StateFailed || it.NextRun == nil {
		t.Fatalf("applied %+v", it)
	}
}

func TestKubeRefusesWithoutToken(t *testing.T) {
	t.Parallel()
	k := kubeServer(t, nil, nil, nil)
	k.Token = func() (string, error) { return "wrong", nil }
	if _, err := k.Jobs(context.Background(), "ns"); err == nil {
		t.Fatal("unauthorised read passed")
	}
}

func TestReporterWritesProgressAndFinish(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var writes [][]byte
	r := &activity.Reporter{Every: 20 * time.Millisecond, Put: func(_ context.Context, b []byte) error {
		mu.Lock()
		writes = append(writes, append([]byte(nil), b...))
		mu.Unlock()
		return nil
	}}
	ctx := context.Background()
	r.Start(ctx)
	var n atomic.Int64
	r.Track("Downloading", "bytes", 100, &n)
	n.Store(40)
	r.Queue(3, "targets")
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	mid, err := activity.ParseReport(writes[len(writes)-1])
	mu.Unlock()
	if err != nil || mid.State != activity.ReportRunning || mid.Done != 40 || mid.Total != 100 || mid.Queued != 3 {
		t.Fatalf("mid report %+v %v", mid, err)
	}
	var it activity.Item
	if !it.FromReport(mid, time.Now()) || it.State != activity.StateRunning || it.Done != 40 || it.Queued != 3 {
		t.Fatalf("item from report %+v", it)
	}
	if it.FromReport(mid, time.Now().Add(activity.StaleAfter+time.Second)) {
		t.Fatal("stale report taken as live")
	}
	r.Finish(ctx, errors.New("bad"))
	mu.Lock()
	end, _ := activity.ParseReport(writes[len(writes)-1])
	mu.Unlock()
	if end.State != activity.ReportFailed || end.Error != "bad" {
		t.Fatalf("end report %+v", end)
	}
	var nilRep *activity.Reporter
	nilRep.Start(ctx)
	nilRep.Set("x", 1, 2, "y")
	nilRep.Finish(ctx, nil)
}
