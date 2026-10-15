package starcat

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// A piece file is:
//
//	magic | zstd block ... | zstd footer JSON | footer offset u64 | footer length u32 | magic
//
// Each block holds up to blockRows rows, column after column, and the
// footer lists every block with its byte range and the cap around its rows.
const (
	magic     = "STARCAT1"
	blockRows = 4096
	sortOrder = 12
	zstdLevel = zstd.SpeedBestCompression
)

var errFormat = errors.New("starcat: not a piece file")

type colKind uint8

const (
	kindI64 colKind = iota + 1
	kindF64
	kindF32
	kindU8
	kindStr
)

type colWriter struct{ b []byte }

func (w *colWriter) section(k colKind, p []byte) {
	w.b = append(w.b, byte(k))
	w.b = binary.AppendUvarint(w.b, uint64(len(p)))
	w.b = append(w.b, p...)
}

func (w *colWriter) i64(v []int64) {
	var p []byte
	prev := int64(0)
	for _, x := range v {
		p = binary.AppendVarint(p, x-prev)
		prev = x
	}
	w.section(kindI64, p)
}

func (w *colWriter) f64(v []float64) {
	n := len(v)
	p := make([]byte, 8*n)
	for i, x := range v {
		b := math.Float64bits(x)
		for j := range 8 {
			p[j*n+i] = byte(b >> (8 * j))
		}
	}
	w.section(kindF64, p)
}

func (w *colWriter) f32(v []float32) {
	n := len(v)
	p := make([]byte, 4*n)
	for i, x := range v {
		b := math.Float32bits(x)
		for j := range 4 {
			p[j*n+i] = byte(b >> (8 * j))
		}
	}
	w.section(kindF32, p)
}

func (w *colWriter) u8(v []uint8) { w.section(kindU8, v) }

func (w *colWriter) str(v []string) {
	var p []byte
	for _, s := range v {
		p = binary.AppendUvarint(p, uint64(len(s)))
	}
	for _, s := range v {
		p = append(p, s...)
	}
	w.section(kindStr, p)
}

type colReader struct {
	b   []byte
	err error
}

func (r *colReader) section(k colKind) []byte {
	if r.err != nil {
		return nil
	}
	if len(r.b) == 0 || colKind(r.b[0]) != k {
		r.err = fmt.Errorf("starcat: expected column kind %d", k)
		return nil
	}
	l, n := binary.Uvarint(r.b[1:])
	if n <= 0 || uint64(len(r.b)-1-n) < l {
		r.err = errors.New("starcat: short column")
		return nil
	}
	p := r.b[1+n : 1+n+int(l)]
	r.b = r.b[1+n+int(l):]
	return p
}

func (r *colReader) i64(n int) []int64 {
	p := r.section(kindI64)
	if r.err != nil {
		return nil
	}
	out := make([]int64, n)
	prev := int64(0)
	for i := range out {
		d, k := binary.Varint(p)
		if k <= 0 {
			r.err = errors.New("starcat: short int column")
			return nil
		}
		p = p[k:]
		prev += d
		out[i] = prev
	}
	return out
}

func (r *colReader) f64(n int) []float64 {
	p := r.section(kindF64)
	if r.err == nil && len(p) != 8*n {
		r.err = errors.New("starcat: float64 column length")
	}
	if r.err != nil {
		return nil
	}
	out := make([]float64, n)
	for i := range out {
		var b uint64
		for j := range 8 {
			b |= uint64(p[j*n+i]) << (8 * j)
		}
		out[i] = math.Float64frombits(b)
	}
	return out
}

func (r *colReader) f32(n int) []float32 {
	p := r.section(kindF32)
	if r.err == nil && len(p) != 4*n {
		r.err = errors.New("starcat: float32 column length")
	}
	if r.err != nil {
		return nil
	}
	out := make([]float32, n)
	for i := range out {
		var b uint32
		for j := range 4 {
			b |= uint32(p[j*n+i]) << (8 * j)
		}
		out[i] = math.Float32frombits(b)
	}
	return out
}

func (r *colReader) u8(n int) []uint8 {
	p := r.section(kindU8)
	if r.err == nil && len(p) != n {
		r.err = errors.New("starcat: uint8 column length")
	}
	return p
}

func (r *colReader) str(n int) []string {
	p := r.section(kindStr)
	if r.err != nil {
		return nil
	}
	lens := make([]uint64, n)
	for i := range lens {
		l, k := binary.Uvarint(p)
		if k <= 0 {
			r.err = errors.New("starcat: short string column")
			return nil
		}
		lens[i], p = l, p[k:]
	}
	out := make([]string, n)
	for i, l := range lens {
		if uint64(len(p)) < l {
			r.err = errors.New("starcat: short string column")
			return nil
		}
		out[i], p = string(p[:l]), p[l:]
	}
	return out
}

// schema describes how one catalogue's rows are stored.
type schema[T any] struct {
	name string
	pos  func(*T) (ra, dec float64)
	key  func(*T) int64
	enc  func(w *colWriter, rows []T)
	dec  func(r *colReader, n int) []T
}

type blockInfo struct {
	Off  int64 `json:"off"`
	Len  int64 `json:"len"`
	Rows int   `json:"rows"`
	Cap  bound `json:"cap"`
}

type footer struct {
	Schema string      `json:"schema"`
	Rows   int         `json:"rows"`
	Blocks []blockInfo `json:"blocks"`
}

var (
	encOnce sync.Once
	encoder *zstd.Encoder
	decOnce sync.Once
	decoder *zstd.Decoder
)

func zenc() *zstd.Encoder {
	encOnce.Do(func() {
		encoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstdLevel), zstd.WithEncoderConcurrency(1))
	})
	return encoder
}

func zdec() *zstd.Decoder {
	decOnce.Do(func() {
		decoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(1<<30))
	})
	return decoder
}

// encodePiece sorts rows by their order-12 pixel and key and writes them
// as a piece file.
func encodePiece[T any](s schema[T], rows []T) ([]byte, error) {
	type keyed struct {
		pix, key int64
		v        [3]float64
	}
	ks := make([]keyed, len(rows))
	idx := make([]int, len(rows))
	for i := range rows {
		ra, dec := s.pos(&rows[i])
		ks[i] = keyed{pix: Pixel(sortOrder, ra, dec), key: s.key(&rows[i]), v: Vec(ra, dec)}
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool {
		x, y := ks[idx[a]], ks[idx[b]]
		if x.pix != y.pix {
			return x.pix < y.pix
		}
		return x.key < y.key
	})
	sorted := make([]T, len(rows))
	vs := make([][3]float64, len(rows))
	for i, j := range idx {
		sorted[i], vs[i] = rows[j], ks[j].v
	}
	out := []byte(magic)
	f := footer{Schema: s.name, Rows: len(rows)}
	for lo := 0; lo < len(sorted); lo += blockRows {
		hi := min(lo+blockRows, len(sorted))
		var w colWriter
		s.enc(&w, sorted[lo:hi])
		z := zenc().EncodeAll(w.b, nil)
		f.Blocks = append(f.Blocks, blockInfo{Off: int64(len(out)), Len: int64(len(z)), Rows: hi - lo, Cap: boundOf(vs[lo:hi])})
		out = append(out, z...)
	}
	fj, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	fz := zenc().EncodeAll(fj, nil)
	off := len(out)
	out = append(out, fz...)
	out = binary.LittleEndian.AppendUint64(out, uint64(off))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(fz)))
	return append(out, magic...), nil
}

func readFooter(b []byte) (footer, error) {
	var f footer
	t := len(b) - 12 - len(magic)
	if t < len(magic) || !bytes.Equal(b[:len(magic)], []byte(magic)) || !bytes.Equal(b[len(b)-len(magic):], []byte(magic)) {
		return f, errFormat
	}
	off := binary.LittleEndian.Uint64(b[t:])
	l := binary.LittleEndian.Uint32(b[t+8:])
	if off+uint64(l) != uint64(t) {
		return f, errFormat
	}
	fj, err := zdec().DecodeAll(b[off:off+uint64(l)], nil)
	if err != nil {
		return f, fmt.Errorf("starcat: footer: %w", err)
	}
	if err := json.Unmarshal(fj, &f); err != nil {
		return f, fmt.Errorf("starcat: footer: %w", err)
	}
	return f, nil
}

// decodePiece calls fn with each block's rows whose cap reaches within r
// radians of v; r < 0 reads every block.
func decodePiece[T any](s schema[T], b []byte, v [3]float64, r float64, fn func([]T)) (int, error) {
	f, err := readFooter(b)
	if err != nil {
		return 0, err
	}
	if f.Schema != s.name {
		return 0, fmt.Errorf("starcat: piece holds %s, not %s", f.Schema, s.name)
	}
	n := 0
	for _, bl := range f.Blocks {
		if r >= 0 && !bl.Cap.reaches(v, r) {
			continue
		}
		if bl.Off < 0 || bl.Off+bl.Len > int64(len(b)) {
			return n, errFormat
		}
		raw, err := zdec().DecodeAll(b[bl.Off:bl.Off+bl.Len], nil)
		if err != nil {
			return n, fmt.Errorf("starcat: block: %w", err)
		}
		cr := &colReader{b: raw}
		rows := s.dec(cr, bl.Rows)
		if cr.err == nil && len(cr.b) != 0 {
			cr.err = errors.New("starcat: trailing bytes in a block")
		}
		if cr.err != nil {
			return n, cr.err
		}
		n += len(rows)
		fn(rows)
	}
	return n, nil
}
