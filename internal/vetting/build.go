package vetting

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// buildRowID is the one row of the announced vetting build.
const buildRowID = 1

// pipelines lists every pipeline this code vets with.
func pipelines() []string {
	return []string{moverPipeline, transientPipeline, starPipeline}
}

// pipelineParts splits a pipeline such as "mover:5" into its kind and
// version.
func pipelineParts(p string) (string, int) {
	kind, v, ok := strings.Cut(p, ":")
	if !ok {
		return p, 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return kind, 0
	}
	return kind, n
}

// newerPipeline says pipeline a is a later version of the same kind as b.
func newerPipeline(a, b string) bool {
	ka, va := pipelineParts(a)
	kb, vb := pipelineParts(b)
	return ka == kb && va > vb
}

// currentPipeline says p is one of the pipelines this code vets with.
func currentPipeline(p string) bool {
	for _, q := range pipelines() {
		if p == q {
			return true
		}
	}
	return false
}

// olderCode says this code is behind the announced build: the
// announcement names a later version of one of its pipelines.
func olderCode(ann app.VetBuild) bool {
	for _, p := range strings.Split(ann.Pipelines, ",") {
		for _, q := range pipelines() {
			if newerPipeline(p, q) {
				return true
			}
		}
	}
	return false
}

// announced is the newest vetting build announced, with false when none
// has been.
func announced(ctx context.Context, db *gorm.DB) (app.VetBuild, bool, error) {
	var row app.VetBuild
	err := db.WithContext(ctx).First(&row, buildRowID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return app.VetBuild{}, false, nil
	}
	if err != nil {
		return app.VetBuild{}, false, err
	}
	return row, true, nil
}

// Announce records build and this code's pipelines as the newest vetting
// code, before a run takes the lock, so a run from an older build still
// going stops before it writes more verdicts. Code behind the announced
// pipelines announces nothing, and the same build announced again keeps
// its first time.
func Announce(ctx context.Context, db *gorm.DB, build string, now time.Time) error {
	ann, ok, err := announced(ctx, db)
	if err != nil {
		return err
	}
	ps := strings.Join(pipelines(), ",")
	if ok && (olderCode(ann) || (ann.Build == build && ann.Pipelines == ps)) {
		return nil
	}
	row := app.VetBuild{ID: buildRowID, Build: build, Pipelines: ps, AnnouncedAt: now}
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"build", "pipelines", "announced_at"}),
	}).Create(&row).Error
}

// superseded says newer vetting code was announced since a run of build
// started at start: a later pipeline, or another build announced after
// it started.
func superseded(ann app.VetBuild, build string, start time.Time) bool {
	if olderCode(ann) {
		return true
	}
	return ann.Build != build && ann.AnnouncedAt.After(start)
}

// staleRow says a stored vetting no longer stands for the current code:
// it came from another pipeline than want, or from another build than
// the announced one after that build was announced. With want empty
// only pipelines this code does not vet with count.
func staleRow(v app.VetBuild, haveAnn bool, row app.DiscoveryVet, want string) bool {
	if want != "" && row.Pipeline != want {
		return !newerPipeline(row.Pipeline, want)
	}
	if want == "" && !currentPipeline(row.Pipeline) {
		return true
	}
	if !haveAnn || row.Build == v.Build || row.FinishedAt == nil {
		return false
	}
	return row.FinishedAt.After(v.AnnouncedAt)
}
