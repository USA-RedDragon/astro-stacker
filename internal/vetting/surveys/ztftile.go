package surveys

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	healpix "github.com/hebl/gohealpix"
)

const (
	tileMagic   = "ZTFT"
	tileVersion = 1
	// tileNside is the HEALPix resolution of the ZTF tiles, about 0.23
	// degrees across.
	tileNside = 256
	// timeUnit is the step of the stored times, in days (0.864 s).
	timeUnit = 1e-5
	// timeBase is the MJD the stored times count from.
	timeBase  = 50000.0
	lightKmS  = 299792.458
	helioStep = 1e4
)

// ztfObject is one ZTF light curve: its object id, mean position, band
// and good epochs, each at mid-exposure in UTC.
type ztfObject struct {
	ID   int64
	RA   float32
	Dec  float32
	Band byte
	T    []uint32
	Mag  []int16
	Err  []uint16
}

func (o *ztfObject) add(mjdUTC, mag, magErr float64) {
	t := math.Round((mjdUTC - timeBase) / timeUnit)
	m := math.Round(mag * 1000)
	e := math.Round(magErr * 1000)
	if t < 0 || t > math.MaxUint32 || m < math.MinInt16 || m > math.MaxInt16 || e <= 0 {
		return
	}
	o.T = append(o.T, uint32(t))
	o.Mag = append(o.Mag, int16(m))
	o.Err = append(o.Err, uint16(math.Min(e, math.MaxUint16)))
}

func (o *ztfObject) mjd(i int) float64 { return timeBase + float64(o.T[i])*timeUnit }

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

func encodeTile(band byte, objs []*ztfObject) []byte {
	n := 0
	for _, o := range objs {
		n += len(o.T)
	}
	b := make([]byte, 0, 16+len(objs)*20+n*8)
	b = append(b, tileMagic...)
	b = append(b, tileVersion, band)
	b = binary.AppendUvarint(b, uint64(len(objs)))
	for _, o := range objs {
		b = binary.AppendUvarint(b, uint64(o.ID))
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(o.RA))
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(o.Dec))
		b = binary.AppendUvarint(b, uint64(len(o.T)))
	}
	for _, o := range objs {
		prev := int64(0)
		for _, t := range o.T {
			b = binary.AppendVarint(b, int64(t)-prev)
			prev = int64(t)
		}
	}
	for _, o := range objs {
		for _, m := range o.Mag {
			b = binary.LittleEndian.AppendUint16(b, uint16(m))
		}
	}
	for _, o := range objs {
		for _, e := range o.Err {
			b = binary.LittleEndian.AppendUint16(b, e)
		}
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

func (r *tileReader) varint() int64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Varint(r.b)
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

func (r *tileReader) u16() uint16 {
	if r.err != nil || len(r.b) < 2 {
		r.err = errTile
		return 0
	}
	v := binary.LittleEndian.Uint16(r.b)
	r.b = r.b[2:]
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
	n := r.uvarint()
	if r.err != nil || n > uint64(len(b)) {
		return nil, errTile
	}
	objs := make([]ztfObject, n)
	counts := make([]int, n)
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
	}
	if r.err != nil {
		return nil, r.err
	}
	want := make([]bool, n)
	for i := range objs {
		want[i] = keep(float64(objs[i].RA), float64(objs[i].Dec))
	}
	for i := range objs {
		prev := int64(0)
		for range counts[i] {
			prev += r.varint()
			if want[i] {
				objs[i].T = append(objs[i].T, uint32(prev))
			}
		}
	}
	for i := range objs {
		for range counts[i] {
			v := r.u16()
			if want[i] {
				objs[i].Mag = append(objs[i].Mag, int16(v))
			}
		}
	}
	for i := range objs {
		for range counts[i] {
			v := r.u16()
			if want[i] {
				objs[i].Err = append(objs[i].Err, v)
			}
		}
	}
	if r.err != nil {
		return nil, fmt.Errorf("%w: %w", errTile, r.err)
	}
	var out []*ztfObject
	for i := range objs {
		if want[i] {
			out = append(out, &objs[i])
		}
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
