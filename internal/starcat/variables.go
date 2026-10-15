package starcat

import (
	"context"
	"sort"
	"time"
)

// VarStar is a row of a survey's catalogue of variable stars: its ID in
// the survey (or its record number when the survey's is not a number), its
// J2000 or ICRS position in degrees, the survey's name and class for it,
// and what the survey measured. A number the survey leaves blank is NaN.
type VarStar struct {
	ID      int64
	Name    string
	RA, Dec float64
	Type    string
	Period  float64
	Mag     float32
	Amp     float32
	Score   float32
}

func varSchema() schema[VarStar] {
	return schema[VarStar]{
		name: "var/1",
		pos:  func(s *VarStar) (float64, float64) { return s.RA, s.Dec },
		key:  func(s *VarStar) int64 { return s.ID },
		enc: func(w *colWriter, rows []VarStar) {
			n := len(rows)
			id := make([]int64, n)
			ra, dec, per := make([]float64, n), make([]float64, n), make([]float64, n)
			mag, amp, score := make([]float32, n), make([]float32, n), make([]float32, n)
			name, typ := make([]string, n), make([]string, n)
			for i, r := range rows {
				id[i], ra[i], dec[i], per[i] = r.ID, r.RA, r.Dec, r.Period
				mag[i], amp[i], score[i], name[i], typ[i] = r.Mag, r.Amp, r.Score, r.Name, r.Type
			}
			w.i64(id)
			w.f64(ra)
			w.f64(dec)
			w.f64(per)
			w.f32(mag)
			w.f32(amp)
			w.f32(score)
			w.str(name)
			w.str(typ)
		},
		dec: func(r *colReader, n int) []VarStar {
			id := r.i64(n)
			ra, dec, per := r.f64(n), r.f64(n), r.f64(n)
			mag, amp, score := r.f32(n), r.f32(n), r.f32(n)
			name, typ := r.str(n), r.str(n)
			if r.err != nil {
				return nil
			}
			out := make([]VarStar, n)
			for i := range out {
				out[i] = VarStar{ID: id[i], Name: name[i], RA: ra[i], Dec: dec[i], Type: typ[i], Period: per[i], Mag: mag[i], Amp: amp[i], Score: score[i]}
			}
			return out
		},
	}
}

// Variables reads one survey's mirrored catalogue of variable stars.
type Variables struct{ t *table[VarStar] }

// NewVariables reads the variable star mirror under prefix in a bucket.
func NewVariables(b Bucket, prefix string) *Variables {
	return &Variables{t: &table[VarStar]{bucket: b, prefix: prefix, schema: varSchema(), now: time.Now}}
}

// Cone returns the stars within radius degrees of (ra, dec) that keep
// accepts (nil keeps all), by ID.
func (v *Variables) Cone(ctx context.Context, ra, dec, radius float64, keep func(*VarStar) bool) ([]VarStar, error) {
	out, err := v.t.cone(ctx, ra, dec, radius, keep)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

// Manifest returns the mirror's manifest.
func (v *Variables) Manifest(ctx context.Context) (*Manifest, error) {
	m, _, err := v.t.manifest(ctx)
	return m, err
}

// WriteVariables stores stars as a complete, verified mirror of a
// catalogue under prefix, for tests.
func WriteVariables(ctx context.Context, b Bucket, prefix string, c *VarCatalog, stars []VarStar) error {
	return writeAll(ctx, b, prefix, c.Name, c.Order, varSchema(), stars)
}
