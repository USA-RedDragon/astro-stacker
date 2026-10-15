// Package surveys reads a star's light curves from ZTF, ATLAS, TESS and
// NEOWISE, keeping what it pulls in MinIO per target field, zstd
// compressed, so later candidates in the same field reuse it.
package surveys

import (
	"context"
	"net/http"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// Field is one target's footprint on the sky, in degrees.
type Field struct {
	Object string  `json:"object"`
	Slug   string  `json:"slug"`
	RA     float64 `json:"ra"`
	Dec    float64 `json:"dec"`
	Radius float64 `json:"radius"`
}

// Neighbour is a Gaia star close to the target star.
type Neighbour struct {
	GaiaID int64   `json:"gaia_id"`
	RA     float64 `json:"ra"`
	Dec    float64 `json:"dec"`
	G      float64 `json:"g"`
	Sep    float64 `json:"sep_arcsec"`
}

// Target is the star to fetch: its Gaia position at epoch 2016.0 with
// proper motion in mas per year, and its neighbours within 10 arcsec.
type Target struct {
	GaiaID     int64       `json:"gaia_id"`
	RA         float64     `json:"ra"`
	Dec        float64     `json:"dec"`
	PMRA       float64     `json:"pmra"`
	PMDec      float64     `json:"pmdec"`
	G          float64     `json:"g"`
	Neighbours []Neighbour `json:"neighbours,omitempty"`
}

// State is how a fetch went.
type State string

const (
	// Done means the survey answered; Series may still be empty.
	Done State = "done"
	// Pending means the request is queued (ATLAS, TESS) and a later run
	// picks up the answer.
	Pending State = "pending"
	// Failed means the survey could not be reached or refused.
	Failed State = "failed"
	// Uncovered means the survey never looked at the spot.
	Uncovered State = "uncovered"
)

// Fetch is one survey's answer for one star.
type Fetch struct {
	Survey string      `json:"survey"`
	State  State       `json:"state"`
	Series []lc.Series `json:"series,omitempty"`
	// Note is a plain sentence on what happened when it is not Done, or
	// on what was used (for example the neighbour fitted apart).
	Note string `json:"note,omitempty"`
	// Sectors are the TESS sectors read.
	Sectors []int `json:"sectors,omitempty"`
}

// Store is the MinIO prefix the caches live under.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte, contentType string) error
	// Sizes sums the bytes stored under prefix, per next path segment.
	Sizes(ctx context.Context, prefix string) (map[string]int64, error)
}

// Fetcher reads the surveys through the field caches.
type Fetcher struct {
	Store Store
	// Prefix is where the caches live, "vetting/cache/".
	Prefix     string
	HTTP       *http.Client
	ATLASToken string
	Now        func() time.Time
}

// ZTF is the star's ZTF light curves, from the field's ZTF files.
func (f *Fetcher) ZTF(ctx context.Context, field Field, t Target) Fetch { return f.ztf(ctx, field, t) }

// ATLAS is the star's ATLAS forced photometry, queued on first use.
func (f *Fetcher) ATLAS(ctx context.Context, field Field, t Target) Fetch {
	return f.atlas(ctx, field, t)
}

// TESS is the star's TESS light curves, fitted apart from its neighbours,
// one series per sector, queued for the TESS job on first use.
func (f *Fetcher) TESS(ctx context.Context, field Field, t Target) Fetch {
	return f.tess(ctx, field, t)
}

// NEOWISE is the star's NEOWISE and AllWISE single exposures, from the
// field's footprint pull.
func (f *Fetcher) NEOWISE(ctx context.Context, field Field, t Target) Fetch {
	return f.neowise(ctx, field, t)
}

// Prefetch pulls a field's ZTF files and NEOWISE footprint once.
func (f *Fetcher) Prefetch(ctx context.Context, field Field) error { return f.prefetch(ctx, field) }
