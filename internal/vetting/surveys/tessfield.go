package surveys

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// TESSFlux is one star's light curve in a TESS sector as the TESS job
// wrote it: BJD, flux and flux error of the good cadences.
type TESSFlux struct {
	GaiaID  int64
	T, F, E []float64
}

func tessFieldDir(field Field, sector int) string {
	return fmt.Sprintf("%s%s/tess/lc/s%04d/", DefaultPrefix, FieldSlug(field), sector)
}

// TESSFieldStars lists the Gaia ids with a light curve of sector in the
// field's TESS cache: the stars the TESS job fitted there.
func (f *Fetcher) TESSFieldStars(ctx context.Context, field Field, sector int) ([]int64, error) {
	sz, err := f.Store.Sizes(ctx, tessFieldDir(field, sector))
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(sz))
	for name := range sz {
		id, err := strconv.ParseInt(strings.TrimSuffix(name, ".csv.zst"), 10, 64)
		if err == nil && strings.HasSuffix(name, ".csv.zst") {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// TESSFieldFlux reads one star's light curve of sector from the field's
// TESS cache, in flux, as other stars to compare the star with.
func (f *Fetcher) TESSFieldFlux(ctx context.Context, field Field, sector int, id int64) (TESSFlux, error) {
	raw, err := f.getZ(ctx, fmt.Sprintf("%s%d.csv.zst", tessFieldDir(field, sector), id))
	if err != nil {
		return TESSFlux{}, err
	}
	out := TESSFlux{GaiaID: id}
	cr := csv.NewReader(bytes.NewReader(raw))
	head, err := cr.Read()
	if err != nil {
		return out, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	for _, c := range []string{"bjd", "flux", "flux_err", "quality"} {
		if _, ok := col[c]; !ok {
			return out, fmt.Errorf("no %s column", c)
		}
	}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
		num := func(c string) float64 {
			v, err := strconv.ParseFloat(strings.TrimSpace(rec[col[c]]), 64)
			if err != nil {
				return math.NaN()
			}
			return v
		}
		q, t, fl, fe := num("quality"), num("bjd"), num("flux"), num("flux_err")
		if q != 0 || math.IsNaN(t) || math.IsNaN(fl) || math.IsInf(fl, 0) {
			continue
		}
		out.T, out.F, out.E = append(out.T, t), append(out.F, fl), append(out.E, fe)
	}
	if !sort.Float64sAreSorted(out.T) {
		idx := make([]int, len(out.T))
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool { return out.T[idx[a]] < out.T[idx[b]] })
		t, fl, fe := make([]float64, len(idx)), make([]float64, len(idx)), make([]float64, len(idx))
		for i, j := range idx {
			t[i], fl[i], fe[i] = out.T[j], out.F[j], out.E[j]
		}
		out.T, out.F, out.E = t, fl, fe
	}
	return out, nil
}
