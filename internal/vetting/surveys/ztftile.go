package surveys

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	healpix "github.com/hebl/gohealpix"
)

const (
	tileMagic   = "ZTFT"
	tileVersion = 2
	// tileNside is the HEALPix resolution of the ZTF tiles, about 0.23
	// degrees across.
	tileNside = 256
	// timeUnit is the step of the stored times, in days (0.864 s).
	timeUnit = 1e-5
	// timeBase is the MJD the stored times count from.
	timeBase = 50000.0
	// errSteps is how many steps of the stored magnitude errors make a
	// factor e; one step is 3%.
	errSteps  = 32
	lightKmS  = 299792.458
	helioStep = 1e4
)

// ztfObject is one ZTF light curve: its object id, mean position, band
// and good epochs, each at mid-exposure in UTC, in time order. Mag is in
// millimag; Err is the error coded on a log scale.
type ztfObject struct {
	ID   int64
	RA   float32
	Dec  float32
	Band byte
	T    []uint32
	Mag  []int16
	Err  []uint8
}

func (o *ztfObject) add(mjdUTC, mag, magErr float64) {
	t := math.Round((mjdUTC - timeBase) / timeUnit)
	m := math.Round(mag * 1000)
	if t < 0 || t > math.MaxUint32 || m < math.MinInt16 || m > math.MaxInt16 || !(magErr > 0) {
		return
	}
	e := math.Round(math.Log(math.Max(magErr*1000, 1)) * errSteps)
	o.T = append(o.T, uint32(t))
	o.Mag = append(o.Mag, int16(m))
	o.Err = append(o.Err, uint8(math.Min(e, math.MaxUint8)))
}

func (o *ztfObject) mjd(i int) float64 { return timeBase + float64(o.T[i])*timeUnit }

func (o *ztfObject) mag(i int) float64 { return float64(o.Mag[i]) / 1000 }

func (o *ztfObject) magErr(i int) float64 { return math.Exp(float64(o.Err[i])/errSteps) / 1000 }

// sort puts the epochs in time order.
func (o *ztfObject) sort() {
	if slices.IsSorted(o.T) {
		return
	}
	idx := make([]int, len(o.T))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return o.T[idx[a]] < o.T[idx[b]] })
	t, m, e := make([]uint32, len(idx)), make([]int16, len(idx)), make([]uint8, len(idx))
	for i, j := range idx {
		t[i], m[i], e[i] = o.T[j], o.Mag[j], o.Err[j]
	}
	o.T, o.Mag, o.Err = t, m, e
}

func vec(ra, dec float64) [3]float64 {
	r, d := ra*math.Pi/180, dec*math.Pi/180
	return [3]float64{math.Cos(d) * math.Cos(r), math.Cos(d) * math.Sin(r), math.Sin(d)}
}

func tileOf(ra, dec float64) int64 { return healpix.Vec2PixNest(tileNside, vec(ra, dec)) }

// tilesNear are the tiles within radius arcsec of a position.
func tilesNear(ra, dec, radius float64) []int64 {
	seen := map[int64]bool{tileOf(ra, dec): true}
	out := []int64{tileOf(ra, dec)}
	c := math.Max(math.Cos(dec*math.Pi/180), 1e-6)
	for k := range 8 {
		a := float64(k) * math.Pi / 4
		d := radius / 3600
		p := tileOf(ra+d*math.Cos(a)/c, math.Max(-90, math.Min(90, dec+d*math.Sin(a))))
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// encodeTile writes a tile: the exposure times every object shares,
// then per object its id, position and epoch count, then per epoch the
// exposure index step, the magnitude in two byte planes and the coded
// error, column by column so zstd finds the repeats.
func encodeTile(band byte, objs []*ztfObject) []byte {
	seen := map[uint32]bool{}
	n := 0
	for _, o := range objs {
		o.sort()
		n += len(o.T)
		for _, t := range o.T {
			seen[t] = true
		}
	}
	exps := make([]uint32, 0, len(seen))
	for t := range seen {
		exps = append(exps, t)
	}
	slices.Sort(exps)
	index := make(map[uint32]int, len(exps))
	for i, t := range exps {
		index[t] = i
	}
	b := make([]byte, 0, 16+len(exps)*4+len(objs)*20+n*5)
	b = append(b, tileMagic...)
	b = append(b, tileVersion, band)
	b = binary.AppendUvarint(b, uint64(len(exps)))
	prev := uint32(0)
	for _, t := range exps {
		b = binary.AppendUvarint(b, uint64(t-prev))
		prev = t
	}
	b = binary.AppendUvarint(b, uint64(len(objs)))
	for _, o := range objs {
		b = binary.AppendUvarint(b, uint64(o.ID))
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(o.RA))
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(o.Dec))
		b = binary.AppendUvarint(b, uint64(len(o.T)))
	}
	for _, o := range objs {
		last := -1
		for _, t := range o.T {
			i := index[t]
			b = binary.AppendUvarint(b, uint64(i-last))
			last = i
		}
	}
	for _, o := range objs {
		for _, m := range o.Mag {
			b = append(b, byte(uint16(m)>>8))
		}
	}
	for _, o := range objs {
		for _, m := range o.Mag {
			b = append(b, byte(uint16(m)))
		}
	}
	for _, o := range objs {
		b = append(b, o.Err...)
	}
	return b
}

var errTile = errors.New("damaged ZTF tile")

type tileReader struct {
	b   []byte
	err error
}

func (r *tileReader) uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err = errTile
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *tileReader) u32() uint32 {
	if r.err != nil || len(r.b) < 4 {
		r.err = errTile
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b)
	r.b = r.b[4:]
	return v
}

func (r *tileReader) bytes(n int) []byte {
	if r.err != nil || len(r.b) < n {
		r.err = errTile
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

// decodeTile reads a tile, keeping the epochs only of objects keep says
// yes to.
func decodeTile(b []byte, keep func(ra, dec float64) bool) ([]*ztfObject, error) {
	if len(b) < 6 || string(b[:4]) != tileMagic || b[4] != tileVersion {
		return nil, errTile
	}
	band := b[5]
	r := &tileReader{b: b[6:]}
	ne := r.uvarint()
	if r.err != nil || ne > uint64(len(b)) {
		return nil, errTile
	}
	exps := make([]uint32, ne)
	t := uint64(0)
	for i := range exps {
		t += r.uvarint()
		exps[i] = uint32(t)
	}
	n := r.uvarint()
	if r.err != nil || n > uint64(len(b)) {
		return nil, errTile
	}
	objs := make([]ztfObject, n)
	counts := make([]int, n)
	total := 0
	for i := range objs {
		objs[i].ID = int64(r.uvarint())
		objs[i].RA = math.Float32frombits(r.u32())
		objs[i].Dec = math.Float32frombits(r.u32())
		objs[i].Band = band
		c := r.uvarint()
		if c > uint64(len(b)) {
			return nil, errTile
		}
		counts[i] = int(c)
		total += int(c)
	}
	if r.err != nil {
		return nil, r.err
	}
	want := make([]bool, n)
	for i := range objs {
		want[i] = keep(float64(objs[i].RA), float64(objs[i].Dec))
	}
	for i := range objs {
		k := -1
		for range counts[i] {
			k += int(r.uvarint())
			if k < 0 || k >= len(exps) {
				return nil, errTile
			}
			if want[i] {
				objs[i].T = append(objs[i].T, exps[k])
			}
		}
	}
	hi, lo, codes := r.bytes(total), r.bytes(total), r.bytes(total)
	if r.err != nil {
		return nil, fmt.Errorf("%w: %w", errTile, r.err)
	}
	var out []*ztfObject
	at := 0
	for i := range objs {
		if want[i] {
			o := &objs[i]
			for j := at; j < at+counts[i]; j++ {
				o.Mag = append(o.Mag, int16(uint16(hi[j])<<8|uint16(lo[j])))
			}
			o.Err = append([]uint8(nil), codes[at:at+counts[i]]...)
			out = append(out, o)
		}
		at += counts[i]
	}
	return out, nil
}

// helio turns ZTF heliocentric dates back into UTC. ZTF's hmjd (and the
// light curve service's hjd) is the mid-exposure UTC date moved to the
// Sun: checked against the service's mjd, the start of a 30 s exposure,
// it is 15.0 s later after removing the light time from Earth to the
// Sun along the line of sight.
type helio struct {
	e     *ephem.Ephemeris
	cache map[int64][3]float64
}

func (f *Fetcher) helio() (*helio, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.eph == nil {
		e, err := ephem.Load()
		if err != nil {
			return nil, err
		}
		f.eph = e
	}
	return &helio{e: f.eph, cache: map[int64][3]float64{}}, nil
}

// utc is the UTC MJD at Earth of a heliocentric MJD toward ra, dec.
func (h *helio) utc(hmjd, ra, dec float64) (float64, error) {
	k := int64(math.Round(hmjd * helioStep))
	v, ok := h.cache[k]
	if !ok {
		et := ephem.ET(lc.MJDTime(float64(k) / helioStep))
		pe, err := h.e.Barycentric(ephem.Earth, et)
		if err != nil {
			return 0, err
		}
		ps, err := h.e.Barycentric(ephem.Sun, et)
		if err != nil {
			return 0, err
		}
		v = [3]float64{pe[0] - ps[0], pe[1] - ps[1], pe[2] - ps[2]}
		h.cache[k] = v
	}
	n := vec(ra, dec)
	return hmjd - (v[0]*n[0]+v[1]*n[1]+v[2]*n[2])/lightKmS/86400, nil
}
