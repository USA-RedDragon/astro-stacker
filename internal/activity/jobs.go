package activity

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	// The image has no zone data for the CronJobs' time zones.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// Watch says which Kubernetes Jobs do one kind of work: those a CronJob
// made, or those with a label.
type Watch struct {
	Kind      string
	Namespace string
	CronJob   string
	Label     string
}

// JobInfo is what the Jobs of a kind are doing.
type JobInfo struct {
	Known        bool
	Running      bool
	Jobs         int
	Workers      int
	Started      *time.Time
	NextRun      *time.Time
	LastFinished *time.Time
	LastFailed   *time.Time
	LastError    string
	Warning      string
}

// Jobs watches the Jobs of each kind.
type Jobs struct {
	Kube    *Kube
	Watches []Watch
	Now     func() time.Time

	mu   sync.Mutex
	info map[string]JobInfo
}

func (j *Jobs) now() time.Time {
	if j.Now != nil {
		return j.Now().UTC()
	}
	return time.Now().UTC()
}

// Info is the last read of kind's Jobs.
func (j *Jobs) Info(kind string) JobInfo {
	if j == nil {
		return JobInfo{}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.info[kind]
}

// Refresh reads every watched namespace once.
func (j *Jobs) Refresh(ctx context.Context) error {
	if j == nil || j.Kube == nil {
		return nil
	}
	namespaces := map[string]bool{}
	for _, w := range j.Watches {
		namespaces[w.Namespace] = true
	}
	jobs := map[string][]KubeJob{}
	crons := map[string]map[string]KubeCronJob{}
	for ns := range namespaces {
		l, err := j.Kube.Jobs(ctx, ns)
		if err != nil {
			return err
		}
		jobs[ns] = l
		c, err := j.Kube.CronJobs(ctx, ns)
		if err != nil {
			return err
		}
		crons[ns] = map[string]KubeCronJob{}
		for _, cj := range c {
			crons[ns][cj.Metadata.Name] = cj
		}
	}
	now := j.now()
	out := map[string]JobInfo{}
	for _, w := range j.Watches {
		var mine []KubeJob
		for _, kj := range jobs[w.Namespace] {
			if w.matches(kj) {
				mine = append(mine, kj)
			}
		}
		info := summarize(mine, now)
		if cj, ok := crons[w.Namespace][w.CronJob]; ok && w.CronJob != "" {
			info.Known = true
			if cj.Spec.Suspend == nil || !*cj.Spec.Suspend {
				if next, err := NextRun(cj.Spec.Schedule, cj.Spec.TimeZone, now); err == nil {
					info.NextRun = &next
				}
			}
			if t := cj.Status.LastSuccessfulTime; t != nil && (info.LastFinished == nil || t.After(*info.LastFinished)) {
				tt := t.UTC()
				info.LastFinished = &tt
			}
		}
		if info.Running {
			if oom := j.oom(ctx, w.Namespace, mine); oom > 0 {
				info.Warning = fmt.Sprintf("%d %s ran out of memory and %s replaced", oom, plural(oom, "worker", "workers"), plural(oom, "was", "were"))
			}
		}
		out[w.Kind] = merge(out[w.Kind], info)
	}
	j.mu.Lock()
	j.info = out
	j.mu.Unlock()
	return nil
}

func (j *Jobs) oom(ctx context.Context, ns string, jobs []KubeJob) int {
	n := 0
	for _, kj := range jobs {
		if _, _, _, done := kj.Finished(); done {
			continue
		}
		pods, err := j.Kube.JobPods(ctx, ns, kj.Metadata.Name)
		if err != nil {
			continue
		}
		n += OOMKilled(pods)
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (w Watch) matches(j KubeJob) bool {
	if w.CronJob != "" && j.CronJob() == w.CronJob {
		return true
	}
	if w.Label == "" {
		return false
	}
	k, v, _ := strings.Cut(w.Label, "=")
	return j.Metadata.Labels[k] == v
}

func summarize(jobs []KubeJob, now time.Time) JobInfo {
	info := JobInfo{Known: len(jobs) > 0}
	var lastOK, lastFail time.Time
	var failMsg string
	for _, kj := range jobs {
		at, failed, reason, done := kj.Finished()
		if !done {
			info.Running = true
			info.Jobs++
			info.Workers += kj.Status.Active
			if s := kj.Status.StartTime; s != nil && (info.Started == nil || s.Before(*info.Started)) {
				t := s.UTC()
				info.Started = &t
			}
			continue
		}
		if failed {
			if at.After(lastFail) {
				lastFail, failMsg = at, reason
			}
		} else if at.After(lastOK) {
			lastOK = at
		}
	}
	if !lastOK.IsZero() {
		t := lastOK.UTC()
		info.LastFinished = &t
	}
	if !lastFail.IsZero() && lastFail.After(lastOK) && now.Sub(lastFail) < 7*24*time.Hour {
		t := lastFail.UTC()
		info.LastFailed, info.LastError = &t, plainJobError(failMsg)
	}
	return info
}

func merge(a, b JobInfo) JobInfo {
	if !a.Known {
		return b
	}
	a.Running = a.Running || b.Running
	a.Jobs += b.Jobs
	a.Workers += b.Workers
	a.Started = earliest(a.Started, b.Started)
	if b.NextRun != nil && (a.NextRun == nil || b.NextRun.Before(*a.NextRun)) {
		a.NextRun = b.NextRun
	}
	a.LastFinished = latest(a.LastFinished, b.LastFinished)
	if b.LastFailed != nil && (a.LastFailed == nil || b.LastFailed.After(*a.LastFailed)) {
		a.LastFailed, a.LastError = b.LastFailed, b.LastError
	}
	if a.Warning == "" {
		a.Warning = b.Warning
	}
	return a
}

func earliest(a, b *time.Time) *time.Time {
	if a == nil || (b != nil && b.Before(*a)) {
		return b
	}
	return a
}

func latest(a, b *time.Time) *time.Time {
	if a == nil || (b != nil && b.After(*a)) {
		return b
	}
	return a
}

func plainJobError(reason string) string {
	switch {
	case strings.Contains(reason, "DeadlineExceeded"), strings.Contains(reason, "active longer than specified deadline"):
		return "ran out of its time limit"
	case strings.Contains(reason, "BackoffLimitExceeded"), strings.Contains(reason, "backoff limit"):
		return "failed too many times"
	case reason == "":
		return "failed"
	}
	return reason
}

// NextRun is when a five-field cron schedule next fires after now, in tz
// (UTC when nil or empty).
func NextRun(schedule string, tz *string, now time.Time) (time.Time, error) {
	loc := time.UTC
	if tz != nil && *tz != "" {
		l, err := time.LoadLocation(*tz)
		if err != nil {
			return time.Time{}, err
		}
		loc = l
	}
	s, err := cron.ParseStandard(schedule)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(now.In(loc)).UTC(), nil
}

// Apply copies what the Jobs say onto it: running or failed, when they
// started, the next run and the last ones.
func (ji JobInfo) Apply(it *Item) {
	if !ji.Known {
		return
	}
	if ji.Running {
		it.State = StateRunning
		if it.Started == nil {
			it.Started = ji.Started
		}
	} else if ji.LastFailed != nil && (ji.LastFinished == nil || ji.LastFailed.After(*ji.LastFinished)) && it.State != StateRunning {
		it.State = StateFailed
	}
	it.NextRun = ji.NextRun
	it.LastFinished = latest(it.LastFinished, ji.LastFinished)
	if ji.LastFailed != nil {
		it.LastFailed, it.LastError = ji.LastFailed, ji.LastError
	}
	if ji.Warning != "" {
		it.Warning = ji.Warning
	}
}

// Run refreshes the Jobs at each interval of every until ctx ends.
func (j *Jobs) Run(ctx context.Context, every time.Duration, onErr func(error)) {
	if j == nil || j.Kube == nil {
		return
	}
	for {
		if err := j.Refresh(ctx); err != nil && ctx.Err() == nil && onErr != nil {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
