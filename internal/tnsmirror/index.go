package tnsmirror

import (
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

// Index answers position searches over every TNS object, sorted by
// declination.
type Index struct {
	objs []Object
	// AsOf is the time of the newest file applied.
	AsOf time.Time
	// Version is the full list's date, YYYYMMDD.
	Version string
}

// Hit is an object near a searched position.
type Hit struct {
	Object
	SepArcsec float64
}

// NewIndex indexes objects; later entries with an objid replace earlier
// ones, as delta files do.
func NewIndex(objs []Object, version string, asOf time.Time) *Index {
	byID := make(map[int64]int, len(objs))
	out := make([]Object, 0, len(objs))
	for _, o := range objs {
		if i, ok := byID[o.ObjID]; ok {
			out[i] = o
			continue
		}
		byID[o.ObjID] = len(out)
		out = append(out, o)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Dec < out[b].Dec })
	return &Index{objs: out, AsOf: asOf, Version: version}
}

// Len is the number of objects.
func (ix *Index) Len() int {
	if ix == nil {
		return 0
	}
	return len(ix.objs)
}

// Near returns the objects within radius arcseconds, nearest first.
func (ix *Index) Near(ra, dec, radiusArcsec float64) []Hit {
	if ix == nil || len(ix.objs) == 0 || !(radiusArcsec > 0) {
		return nil
	}
	r := radiusArcsec / 3600
	lo := sort.Search(len(ix.objs), func(i int) bool { return ix.objs[i].Dec >= dec-r })
	var out []Hit
	for i := lo; i < len(ix.objs) && ix.objs[i].Dec <= dec+r; i++ {
		o := ix.objs[i]
		if sep := starcat.Separation(ra, dec, o.RA, o.Dec) * 3600; sep <= radiusArcsec && !math.IsNaN(sep) {
			out = append(out, Hit{Object: o, SepArcsec: sep})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].SepArcsec < out[b].SepArcsec })
	return out
}
