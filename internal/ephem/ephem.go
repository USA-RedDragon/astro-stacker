// Package ephem computes positions of the Sun, Moon and planets from an
// embedded slice of JPL's DE440s ephemeris (Chebyshev records, SPK type 2,
// 1970 to 2090). Positions are astrometric ICRF (light-time corrected, no
// aberration), topocentric for a site, the frame plate solves and the
// scheduler use. Reductions: IAU 1976 precession, GMST with UT1 = UTC,
// nutation and polar motion ignored; the error this leaves is under 2 arcsec
// for the Moon and well under 1 arcsec for the planets.
package ephem

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/klauspost/compress/zstd"
)

//go:embed data/de440s.bin.zst
var embedded []byte

const (
	Magic = "DE440S1\n"
	// TTMinusUTC is 37 leap seconds plus 32.184 s, valid since 2017.
	TTMinusUTC = 69.184
	j2000Unix  = 946728000.0
)

var (
	ErrOutOfRange = errors.New("the time is outside the embedded ephemeris")
	ErrNoBody     = errors.New("the ephemeris has no such body")
)

type Body int32

const (
	Mercury Body = 1
	Venus   Body = 2
	EMB     Body = 3
	Mars    Body = 4
	Jupiter Body = 5
	Saturn  Body = 6
	Uranus  Body = 7
	Neptune Body = 8
	Sun     Body = 10
	Moon    Body = 301
	Earth   Body = 399
)

func (b Body) String() string {
	switch b {
	case Mercury:
		return "Mercury"
	case Venus:
		return "Venus"
	case EMB:
		return "Earth-Moon barycentre"
	case Mars:
		return "Mars"
	case Jupiter:
		return "Jupiter"
	case Saturn:
		return "Saturn"
	case Uranus:
		return "Uranus"
	case Neptune:
		return "Neptune"
	case Sun:
		return "Sun"
	case Moon:
		return "Moon"
	case Earth:
		return "Earth"
	}
	return fmt.Sprintf("body %d", int32(b))
}

// Planets are the planets the event finder looks at, Mercury to Neptune.
func Planets() []Body {
	return []Body{Mercury, Venus, Mars, Jupiter, Saturn, Uranus, Neptune}
}

// ET is seconds of TDB past J2000, taking TDB as TT.
func ET(t time.Time) float64 {
	return float64(t.UTC().UnixNano())/1e9 - j2000Unix + TTMinusUTC
}

func timeOf(et float64) time.Time {
	s := et + j2000Unix - TTMinusUTC
	return time.Unix(0, int64(math.Round(s*1e9))).UTC()
}

type SegmentHeader struct {
	Target, Center int32
	Init, IntLen   float64
	RSize, N       int32
}

type segment struct {
	SegmentHeader
	coeffs []float64
}

func (s segment) start() float64 { return s.Init }
func (s segment) stop() float64  { return s.Init + float64(s.N)*s.IntLen }

func (s segment) position(et float64) (Vec3, bool) {
	if et < s.start() || et > s.stop() {
		return Vec3{}, false
	}
	i := min(int32((et-s.Init)/s.IntLen), s.N-1)
	rec := s.coeffs[int(i)*int(s.RSize) : int(i+1)*int(s.RSize)]
	mid, radius := rec[0], rec[1]
	n := int(s.RSize-2) / 3
	x := (et - mid) / radius
	var out Vec3
	for axis := range 3 {
		c := rec[2+axis*n : 2+(axis+1)*n]
		t0, t1 := 1.0, x
		sum := c[0]
		if n > 1 {
			sum += c[1] * x
		}
		for k := 2; k < n; k++ {
			t2 := 2*x*t1 - t0
			sum += c[k] * t2
			t0, t1 = t1, t2
		}
		out[axis] = sum
	}
	return out, true
}

type Ephemeris struct {
	segs     map[int32]segment
	from, to float64
}

// Load reads the embedded ephemeris.
func Load() (*Ephemeris, error) {
	return Read(embedded)
}

// Read reads an ephemeris written by the gen tool.
func Read(compressed []byte) (*Ephemeris, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("decompress ephemeris: %w", err)
	}
	if !bytes.HasPrefix(raw, []byte(Magic)) {
		return nil, errors.New("not an ephemeris file")
	}
	r := bytes.NewReader(raw[len(Magic):])
	var count int32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return nil, err
	}
	e := &Ephemeris{segs: map[int32]segment{}, from: math.Inf(-1), to: math.Inf(1)}
	for range count {
		var h SegmentHeader
		if err := binary.Read(r, binary.LittleEndian, &h); err != nil {
			return nil, err
		}
		if h.RSize < 5 || h.N <= 0 || (h.RSize-2)%3 != 0 {
			return nil, fmt.Errorf("segment %d: bad record layout", h.Target)
		}
		s := segment{SegmentHeader: h, coeffs: make([]float64, int(h.N)*int(h.RSize))}
		if err := binary.Read(r, binary.LittleEndian, s.coeffs); err != nil {
			return nil, fmt.Errorf("segment %d: %w", h.Target, err)
		}
		e.segs[h.Target] = s
		e.from, e.to = math.Max(e.from, s.start()), math.Min(e.to, s.stop())
	}
	if _, err := r.ReadByte(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing bytes after the last segment")
	}
	for _, b := range []Body{Sun, EMB, Earth, Moon, Mercury, Venus, Mars, Jupiter, Saturn, Uranus, Neptune} {
		if _, ok := e.segs[int32(b)]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrNoBody, b)
		}
	}
	return e, nil
}

// Range is the span the ephemeris covers.
func (e *Ephemeris) Range() (time.Time, time.Time) {
	return timeOf(e.from), timeOf(e.to)
}

// Covers reports whether t, and the light time around it, is in range.
func (e *Ephemeris) Covers(t time.Time) bool {
	et := ET(t)
	return et-86400 >= e.from && et+86400 <= e.to
}

// Barycentric is the body's position relative to the solar-system
// barycentre, in km.
func (e *Ephemeris) Barycentric(b Body, et float64) (Vec3, error) {
	var out Vec3
	target := int32(b)
	for target != 0 {
		s, ok := e.segs[target]
		if !ok {
			return out, fmt.Errorf("%w: %d", ErrNoBody, target)
		}
		p, ok := s.position(et)
		if !ok {
			return out, ErrOutOfRange
		}
		out = out.add(p)
		target = s.Center
	}
	return out, nil
}
