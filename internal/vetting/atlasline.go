package vetting

import (
	"context"
	"strings"
	"sync"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
	"gorm.io/gorm"
)

// Places in line for room in the ATLAS queue, first to last. ATLAS runs
// one of our tasks at a time, so a place decides when the answer comes.
const (
	// atlasAsked is a source the user asked to have vetted again.
	atlasAsked surveys.Priority = iota
	// atlasOpen is an unvetted source whose last vet left it open, real
	// or unsure: in Needs a look, Watching, Low or Not sure, where ATLAS
	// can still move it and the user sees the move.
	atlasOpen
	// atlasNew is a source never vetted yet.
	atlasNew
	// atlasRefresh is a source the user marked real, whose old ATLAS
	// result is in use until the weekly refresh replaces it.
	atlasRefresh
	// atlasSettled is an unvetted source its last vet called an artifact
	// with medium or high confidence, or known, which ATLAS seldom moves,
	// or one held back at low confidence, which no view of new sources
	// shows.
	atlasSettled
)

// atlasPlace is where a due source stands in line for room in the ATLAS
// queue.
func atlasPlace(d Due) surveys.Priority {
	switch {
	case d.AskedAt != nil:
		return atlasAsked
	case d.Summary.Source.Vetting != "":
		return atlasRefresh
	case d.Summary.Class == discoveries.ClassLowConfidence:
		return atlasSettled
	case d.Prior == nil || d.Prior.Suggested == "":
		return atlasNew
	case d.Prior.Suggested == app.VetKnown,
		d.Prior.Suggested == app.VetArtifact && (d.Prior.Confidence == Medium || d.Prior.Confidence == High):
		return atlasSettled
	}
	return atlasOpen
}

// waitsOnATLAS says the source's last vet waited on ATLAS, or it was
// never vetted, so its next vet may ask ATLAS for room.
func waitsOnATLAS(d Due) bool {
	return d.Prior == nil || pendingHas(d.Prior.Pending, lc.ATLAS)
}

func pendingHas(pending, survey string) bool {
	for p := range strings.SplitSeq(pending, ",") {
		if strings.TrimSpace(p) == survey {
			return true
		}
	}
	return false
}

// atlasLine counts, per place, the sources a run is still to vet that
// may ask ATLAS for room, so room that frees while the run goes is kept
// for the better placed ones still to come.
type atlasLine struct {
	mu   sync.Mutex
	left map[surveys.Priority]int
}

func newATLASLine(dues []Due) *atlasLine {
	l := &atlasLine{left: map[surveys.Priority]int{}}
	for _, d := range dues {
		if waitsOnATLAS(d) {
			l.left[atlasPlace(d)]++
		}
	}
	return l
}

// visit takes a source out of the count as its vet starts.
func (l *atlasLine) visit(d Due) {
	if l == nil || !waitsOnATLAS(d) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if p := atlasPlace(d); l.left[p] > 0 {
		l.left[p]--
	}
}

// ahead is how many sources still to vet stand before place p.
func (l *atlasLine) ahead(p surveys.Priority) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for q, c := range l.left {
		if q < p {
			n += c
		}
	}
	return n
}

// ATLASWaiting counts the sources the job still vets whose last vet
// waits on ATLAS.
func ATLASWaiting(ctx context.Context, db *gorm.DB) (int64, error) {
	var n int64
	err := db.WithContext(ctx).Raw(`SELECT count(*) FROM discovery_vets v JOIN discovery_sources s ON s.id = v.source_id
		WHERE ',' || v.pending || ',' LIKE ? AND (s.vetting = '' OR (s.vetting = ? AND s.kind <> ?))`,
		"%,"+lc.ATLAS+",%", app.VetReal, kindMover).Scan(&n).Error
	return n, err
}
