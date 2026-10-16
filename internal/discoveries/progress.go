package discoveries

import (
	"context"
	"time"

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
	// Due is the unsettled nights the server's runner takes: within the
	// backfill window or requested.
	Due int64
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

// ReadProgress counts the comparisons at now; nights from since on are in
// the server runner's backfill window.
func ReadProgress(ctx context.Context, db *gorm.DB, now, since time.Time) (Progress, error) {
	var p Progress
	settled := `(method = ? AND (state IN (?, ?) OR (state = ? AND (attempts >= ? OR next_try > ?))))`
	err := db.WithContext(ctx).Raw(`SELECT count(*) AS nights,
		count(*) FILTER (WHERE ok) AS settled,
		count(*) FILTER (WHERE old) AS old,
		count(*) FILTER (WHERE gave_up) AS gave_up,
		count(*) FILTER (WHERE NOT ok AND (night >= ? OR requested)) AS due
		FROM (SELECT object, night,
			bool_and(`+settled+`) AS ok,
			bool_or(method < ?) AS old,
			bool_or(state = ? AND attempts >= ?) AS gave_up,
			bool_or(requested) AS requested
			FROM discovery_runs GROUP BY object, night) t`,
		since, Method, app.DiscoveryDone, app.DiscoverySkipped, app.DiscoveryFailed, maxAttempts, now,
		Method, app.DiscoveryFailed, maxAttempts).Row().Scan(&p.Nights, &p.Settled, &p.Old, &p.GaveUp, &p.Due)
	if err != nil {
		return p, err
	}
	var rows []app.DiscoveryRun
	err = db.WithContext(ctx).Select("object", "night", "filter_group", "stage").
		Where("state = ? AND updated_at > ?", app.DiscoveryRunning, now.Add(-claimFor)).
		Order("updated_at DESC").Find(&rows).Error
	seen := map[string]bool{}
	for _, r := range rows {
		p.Running = append(p.Running, RunningNight{Object: r.Object, Night: r.Night, Group: r.Group, Stage: r.Stage})
		if k := r.Object + "\x00" + r.Night.Format(time.DateOnly); !seen[k] {
			seen[k] = true
			p.RunningNights++
		}
	}
	return p, err
}
