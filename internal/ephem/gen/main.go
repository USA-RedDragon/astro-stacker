package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/klauspost/compress/zstd"
)

const recordBytes = 1024

type summary struct {
	start, stop         float64
	target, center      int32
	frame, kind         int32
	startAddr, stopAddr int32
}

func main() {
	in := flag.String("in", "de440s.bsp", "JPL DE440s SPK file (https://naif.jpl.nasa.gov/pub/naif/generic_kernels/spk/planets/de440s.bsp)")
	out := flag.String("out", "internal/ephem/data/de440s.bin.zst", "output file")
	from := flag.String("from", "2025-01-01", "first UTC date kept")
	to := flag.String("to", "2041-01-01", "UTC date after the last kept")
	flag.Parse()
	if err := run(*in, *out, *from, *to); err != nil {
		log.Fatal(err)
	}
}

func run(in, out, from, to string) error {
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	start, err := time.Parse(time.DateOnly, from)
	if err != nil {
		return err
	}
	stop, err := time.Parse(time.DateOnly, to)
	if err != nil {
		return err
	}
	sums, err := summaries(raw)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString(ephem.Magic)
	count := int32(0)
	for range sums {
		count++
	}
	if err := binary.Write(&buf, binary.LittleEndian, count); err != nil {
		return err
	}
	lo, hi := ephem.ET(start), ephem.ET(stop)
	for _, s := range sums {
		if s.kind != 2 {
			return fmt.Errorf("segment %d: SPK type %d, want 2", s.target, s.kind)
		}
		seg, err := trim(raw, s, lo, hi)
		if err != nil {
			return err
		}
		if err := binary.Write(&buf, binary.LittleEndian, seg.header()); err != nil {
			return err
		}
		if err := binary.Write(&buf, binary.LittleEndian, seg.coeffs); err != nil {
			return err
		}
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	defer enc.Close()
	return os.WriteFile(out, enc.EncodeAll(buf.Bytes(), nil), 0o600)
}

type segment struct {
	target, center int32
	init, intlen   float64
	rsize, n       int32
	coeffs         []float64
}

func (s segment) header() ephem.SegmentHeader {
	return ephem.SegmentHeader{Target: s.target, Center: s.center, Init: s.init, IntLen: s.intlen, RSize: s.rsize, N: s.n}
}

func word(raw []byte, addr int32) (float64, error) {
	off := int64(addr-1) * 8
	if off < 0 || off+8 > int64(len(raw)) {
		return 0, io.ErrUnexpectedEOF
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(raw[off:])), nil
}

func trim(raw []byte, s summary, lo, hi float64) (segment, error) {
	var tail [4]float64
	for i := range tail {
		v, err := word(raw, s.stopAddr-3+int32(i))
		if err != nil {
			return segment{}, err
		}
		tail[i] = v
	}
	init, intlen, rsize, n := tail[0], tail[1], int32(tail[2]), int32(tail[3])
	first := max(int32(math.Floor((lo-init)/intlen)), 0)
	last := min(int32(math.Ceil((hi-init)/intlen)), n)
	if last <= first {
		return segment{}, fmt.Errorf("segment %d does not cover the range", s.target)
	}
	seg := segment{target: s.target, center: s.center, init: init + float64(first)*intlen, intlen: intlen, rsize: rsize, n: last - first}
	seg.coeffs = make([]float64, 0, int(seg.n)*int(rsize))
	for r := first; r < last; r++ {
		for k := range rsize {
			v, err := word(raw, s.startAddr+r*rsize+k)
			if err != nil {
				return segment{}, err
			}
			seg.coeffs = append(seg.coeffs, v)
		}
	}
	return seg, nil
}

func summaries(raw []byte) ([]summary, error) {
	if len(raw) < recordBytes || string(raw[:7]) != "DAF/SPK" {
		return nil, errors.New("not a DAF/SPK file")
	}
	if string(raw[88:96]) != "LTL-IEEE" {
		return nil, errors.New("not a little-endian SPK file")
	}
	nd, ni := i32(raw[8:]), i32(raw[12:])
	if nd != 2 || ni != 6 {
		return nil, fmt.Errorf("ND=%d NI=%d, want 2 and 6", nd, ni)
	}
	next := i32(raw[76:])
	var out []summary
	for next > 0 {
		base := int64(next-1) * recordBytes
		if base+recordBytes > int64(len(raw)) {
			return nil, io.ErrUnexpectedEOF
		}
		rec := raw[base : base+recordBytes]
		next = int32(math.Float64frombits(binary.LittleEndian.Uint64(rec[0:])))
		count := int(math.Float64frombits(binary.LittleEndian.Uint64(rec[16:])))
		for i := range count {
			p := rec[24+i*40:]
			ints := p[16:]
			out = append(out, summary{
				start: math.Float64frombits(binary.LittleEndian.Uint64(p[0:])), stop: math.Float64frombits(binary.LittleEndian.Uint64(p[8:])),
				target: i32(ints[0:]), center: i32(ints[4:]),
				frame: i32(ints[8:]), kind: i32(ints[12:]),
				startAddr: i32(ints[16:]), stopAddr: i32(ints[20:]),
			})
		}
	}
	return out, nil
}

func i32(b []byte) int32 {
	var v int32
	_ = binary.Read(bytes.NewReader(b[:4]), binary.LittleEndian, &v)
	return v
}
