// Package surveys reads a star's light curves from ZTF, ATLAS, TESS and
// NEOWISE, keeping what it pulls in MinIO per target field, zstd
// compressed, so later candidates in the same field reuse it.
package surveys

import (
	"context"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/klauspost/compress/zstd"
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

const (
	// PairArcsec is how close a Gaia neighbour sits for ZTF, with about 2
	// arcsec seeing, to measure it and the star as one object.
	PairArcsec = 3.0
	// PairMag is how much fainter than the star such a neighbour may be
	// and still give a tenth of the light.
	PairMag = 2.5
)

// Pair is the one Gaia neighbour within PairArcsec of the star and under
// PairMag fainter than it, or false when there is none or more than one.
func Pair(t Target) (Neighbour, bool) {
	var out Neighbour
	n := 0
	if !(t.G > 0) {
		return out, false
	}
	for _, nb := range t.Neighbours {
		sep := nb.Sep
		if sep == 0 {
			sep = sepArcsec(t.RA, t.Dec, nb.RA, nb.Dec)
		}
		if nb.GaiaID == t.GaiaID || sep > PairArcsec || !(nb.G > 0) || nb.G > t.G+PairMag {
			continue
		}
		out = nb
		out.Sep = sep
		n++
	}
	return out, n == 1
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
	// IRSAURL overrides https://irsa.ipac.caltech.edu, for tests.
	IRSAURL string
	// ATLASURL overrides the ATLAS forced photometry server, for tests.
	ATLASURL string
	// WorkDir holds the ZTF field files while Prefetch reads them; empty
	// is the system temporary directory.
	WorkDir string
	// ZTFRelease names the ZTF data release to pull, "dr24"; empty takes
	// the newest one IRSA lists.
	ZTFRelease string
	// ATLASMaxQueued is how many of our tasks may wait in the ATLAS queue
	// before no more are queued; zero is DefaultATLASMaxQueued.
	ATLASMaxQueued int
	// ATLASAhead says how many stars of better priority than p this run
	// is still to ask of ATLAS; that much room in the ATLAS queue is kept
	// for them. Nil keeps none.
	ATLASAhead func(p Priority) int

	mu        sync.Mutex
	wants     map[string]atlasWant
	wantSeq   int
	eph       *ephem.Ephemeris
	atlasMu   sync.Mutex
	atlasGap  time.Duration
	lastATLAS time.Time
	pullMu    sync.Mutex
	zmu       sync.Mutex
	encs      map[zstd.EncoderLevel]*zstd.Encoder
	dec       *zstd.Decoder
}

// ZTF is the star's ZTF light curves, from the field's ZTF files. A star
// ZTF measures together with one close Gaia neighbour (see Pair) also gets
// ZTF's position of it on each epoch, from IRSA's light curve service.
func (f *Fetcher) ZTF(ctx context.Context, field Field, t Target) Fetch {
	return f.ztf(ctx, field, t, true)
}

// ZTFStars is the ZTF light curves of several stars of one field, read
// together so each of the field's tiles is read once.
func (f *Fetcher) ZTFStars(ctx context.Context, field Field, ts []Target) []Fetch {
	return f.ztfMany(ctx, field, ts, false)
}

// ZTFStarsFromFiles is ZTFStars from the field's ZTF files alone: a star
// outside a complete pull is left uncovered rather than asked of IRSA.
func (f *Fetcher) ZTFStarsFromFiles(ctx context.Context, field Field, ts []Target) []Fetch {
	return f.ztfMany(ctx, field, ts, true)
}

// ATLAS is the star's ATLAS forced photometry, queued on first use.
func (f *Fetcher) ATLAS(ctx context.Context, field Field, t Target) Fetch {
	return f.atlas(ctx, field, t, func(text []byte) Fetch { return atlasFetch(t, text) })
}

// ATLASRows is ATLAS forced photometry at a position, queued on first use
// like ATLAS, as the rows passing the quality cuts: for a spot with no
// star to scale the fluxes by.
func (f *Fetcher) ATLASRows(ctx context.Context, field Field, t Target) (Fetch, []ATLASRow) {
	var rows []ATLASRow
	fe := f.atlas(ctx, field, t, func(text []byte) Fetch {
		all, err := parseATLAS(text)
		if err != nil {
			return failed(lc.ATLAS, "could not read the ATLAS result: %v", err)
		}
		for _, r := range all {
			if r.Good() && !math.IsNaN(r.UJy) && !math.IsNaN(r.DUJy) {
				rows = append(rows, r)
			}
		}
		return Fetch{Survey: lc.ATLAS, State: Done}
	})
	return fe, rows
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
