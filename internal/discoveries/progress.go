package discoveries

import (
	"context"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// Progress counts the stored night comparisons for the processing
// indicator: a night counts once, settled when each of its filter groups
// is settled by the current method.
type Progress struct {
	Nights  int64
	Settled int64
	// Old is the nights with a group last compared by an older method.
	Old int64
	// GaveUp is the nights with a group that failed every attempt.
	GaveUp int64
	// Due is the unsettled nights the server's runner takes now and no
	// runner holds: within the backfill window once their stacking settled,
	// requested and still queued, or waiting to ask ZTF again.
	Due int64
	// Settling is the unsettled nights within the backfill window that wait
	// for their imaging and stacking to settle.
	Settling int64
	// NoSubs is the unsettled nights no runner takes, as the night has no
	// accepted subs in the group or the group too few nights.
	NoSubs int64
	// RunningNights counts the distinct nights in Running.
	RunningNights int64
	Running       []RunningNight
}

// RunningNight is a comparison some runner holds now.
type RunningNight struct {
	Object string
	Night  time.Time
	Group  string
	Stage  string
}

type nightState struct {
	ok, old, gaveUp, due, running, noSubs bool
}

// progressRules are what the runners take, as ReadProgress applies them.
type progressRules struct {
	now, since time.Time
	settled    map[string]bool
	subNights  map[string]map[string]map[time.Time]bool
	stored     map[string]bodies.Target
	movers     map[string]bool
}

// eligible says whether a pass would compare the run's night: it has
// accepted subs in the group, and the group enough nights.
func (pr *progressRules) eligible(run app.DiscoveryRun) bool {
	nights := pr.subNights[run.Object][run.Group]
	return nights[dateOnly(run.Night)] && len(nights) >= MinNights
}

func (pr *progressRules) mover(object string) bool {
	m, seen := pr.movers[object]
	if !seen {
		_, m = bodies.OwnBody(pr.stored, object)
		pr.movers[object] = m
	}
	return m
}

// note adds one run to its night. A moving target's run waits on nothing
// unless it is requested and queued, as no pass takes it otherwise.
func (pr *progressRules) note(n *nightState, run app.DiscoveryRun, key string) (live bool) {
	live = run.State == app.DiscoveryRunning && run.UpdatedAt.After(pr.now.Add(-claimFor))
	n.running = n.running || live
	asked := run.Requested && run.State == app.DiscoveryQueued
	n.gaveUp = n.gaveUp || (run.State == app.DiscoveryFailed && run.Attempts >= maxAttempts)
	mover := pr.mover(run.Object)
	if mover && !asked && !live {
		return live
	}
	eligible := pr.eligible(run)
	recheck := ztfRecheck(run, pr.now) && eligible
	if run.Method == Method && settledRun(run, pr.now) && !recheck {
		return live
	}
	n.ok = false
	n.old = n.old || run.Method < Method
	inWindow := !run.Night.Before(pr.since)
	switch {
	case asked:
		n.due = true
	case mover:
	case !eligible:
		n.noSubs = true
	case (recheck && !inWindow) || (inWindow && pr.settled[key]):
		n.due = true
	}
	return live
}

func (p *Progress) tally(n *nightState, settled map[string]bool, key string) {
	p.Nights++
	switch {
	case n.ok:
		p.Settled++
	case n.running:
	case n.due:
		p.Due++
	default:
		if s, has := settled[key]; has && !s {
			p.Settling++
		} else if n.noSubs {
			p.NoSubs++
		}
	}
	if n.old {
		p.Old++
	}
	if n.gaveUp {
		p.GaveUp++
	}
	if n.running {
		p.RunningNights++
	}
}

// ReadProgress counts the comparisons at now; nights from since on are in
// the server runner's backfill window. stored are the scheduler's targets:
// a moving target's nights are compared only on request.
func ReadProgress(ctx context.Context, db *gorm.DB, now, since time.Time, stored map[string]bodies.Target) (Progress, error) {
	var p Progress
	var runs []app.DiscoveryRun
	if err := db.WithContext(ctx).Select("object", "night", "filter_group", "method", "state", "stage", "attempts", "next_try", "requested", "failed",
		"ztf_failures", "updated_at").Order("updated_at DESC").Find(&runs).Error; err != nil {
		return p, err
	}
	settled, err := settledNights(ctx, db, now, since)
	if err != nil {
		return p, err
	}
	subNights, err := acceptedNights(ctx, db)
	if err != nil {
		return p, err
	}
	pr := &progressRules{now: now, since: since, settled: settled, subNights: subNights, stored: stored, movers: map[string]bool{}}
	nights := map[string]*nightState{}
	var order []string
	for _, run := range runs {
		k := run.Object + "\x00" + dateOnly(run.Night).Format(time.DateOnly)
		n := nights[k]
		if n == nil {
			n = &nightState{ok: true}
			nights[k] = n
			order = append(order, k)
		}
		if pr.note(n, run, k) {
			p.Running = append(p.Running, RunningNight{Object: run.Object, Night: run.Night, Group: run.Group, Stage: run.Stage})
		}
	}
	for _, k := range order {
		p.tally(nights[k], settled, k)
	}
	return p, nil
}

// acceptedNights is the nights with accepted subs of each target's filter
// groups.
func acceptedNights(ctx context.Context, db *gorm.DB) (map[string]map[string]map[time.Time]bool, error) {
	var rows []struct {
		Object string
		Filter string
		Night  time.Time
	}
	if err := acceptedSubs(ctx, db).Distinct("s.object", "s.filter", "f.night").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load nights with subs: %w", err)
	}
	out := map[string]map[string]map[time.Time]bool{}
	for _, r := range rows {
		g := GroupOf(r.Filter)
		if out[r.Object] == nil {
			out[r.Object] = map[string]map[time.Time]bool{}
		}
		if out[r.Object][g] == nil {
			out[r.Object][g] = map[time.Time]bool{}
		}
		out[r.Object][g][dateOnly(r.Night)] = true
	}
	return out, nil
}
