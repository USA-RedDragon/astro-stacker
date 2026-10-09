// Package imagedata decodes the pixels of XISF and FITS frames into
// normalized float32 samples, enough to render previews.
package imagedata

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

// Image holds planar samples normalized to [0, 1]: Data[c*W*H + y*W + x].
type Image struct {
	W, H, C int
	Data    []float32
}

// Plane returns channel c.
func (im *Image) Plane(c int) []float32 {
	n := im.W * im.H
	return im.Data[c*n : (c+1)*n]
}

var ErrUnsupported = errors.New("unsupported image")

// Decode reads a whole XISF or FITS file.
func Decode(b []byte) (*Image, error) {
	switch {
	case bytes.HasPrefix(b, []byte("XISF0100")):
		return decodeXISF(b)
	case bytes.HasPrefix(b, []byte("SIMPLE  =")):
		return decodeFITS(b)
	default:
		return nil, fmt.Errorf("%w: unknown format", ErrUnsupported)
	}
}

type xisfImage struct {
	Geometry     string         `xml:"geometry,attr"`
	SampleFormat string         `xml:"sampleFormat,attr"`
	Location     string         `xml:"location,attr"`
	Compression  string         `xml:"compression,attr"`
	Subblocks    string         `xml:"subblocks,attr"`
	ByteOrder    string         `xml:"byteOrder,attr"`
	Bounds       string         `xml:"bounds,attr"`
	Properties   []xisfProperty `xml:"Property"`
}

type xisfProperty struct {
	ID    string `xml:"id,attr"`
	Value string `xml:"value,attr"`
}

type xisfDoc struct {
	Images []xisfImage `xml:"Image"`
}

// decodeXISF decodes the first image of a monolithic XISF file (an 8-byte
// signature, a little-endian uint32 header length, 4 reserved bytes, the XML
// header, then attached data blocks).
func decodeXISF(b []byte) (*Image, error) {
	if len(b) < 16 {
		return nil, fmt.Errorf("%w: truncated", ErrUnsupported)
	}
	n := int(binary.LittleEndian.Uint32(b[8:12]))
	if len(b) < 16+n {
		return nil, fmt.Errorf("%w: truncated header", ErrUnsupported)
	}
	var doc xisfDoc
	if err := xml.Unmarshal(bytes.TrimRight(b[16:16+n], "\x00"), &doc); err != nil {
		return nil, fmt.Errorf("xisf header: %w", err)
	}
	if len(doc.Images) == 0 {
		return nil, fmt.Errorf("%w: no Image element", ErrUnsupported)
	}
	img := doc.Images[0]

	w, h, c, err := parseGeometry(img.Geometry)
	if err != nil {
		return nil, err
	}
	pos, size, err := parseAttachment(img.Location)
	if err != nil {
		return nil, err
	}
	if pos+size > len(b) {
		return nil, fmt.Errorf("%w: data block past end of file", ErrUnsupported)
	}
	raw := b[pos : pos+size]

	sampleSize, err := sampleBytes(img.SampleFormat)
	if err != nil {
		return nil, err
	}
	want := w * h * c * sampleSize
	if img.Compression != "" {
		raw, err = decompress(raw, img.Compression, img.Subblocks, want)
		if err != nil {
			return nil, err
		}
	}
	if len(raw) != want {
		return nil, fmt.Errorf("%w: have %d bytes, want %d", ErrUnsupported, len(raw), want)
	}
	var order binary.ByteOrder = binary.LittleEndian
	if img.ByteOrder == "big" {
		order = binary.BigEndian
	}
	lo, hi := 0.0, 1.0
	if img.Bounds != "" {
		if l, u, ok := parseBounds(img.Bounds); ok {
			lo, hi = l, u
		}
	}
	if step, zero, empty, ok := codeMapping(img); ok {
		count := w * h * c
		im := &Image{W: w, H: h, C: c, Data: make([]float32, count)}
		for i := range count {
			im.Data[i] = decodeCode(order.Uint16(raw[2*i:]), step, zero, empty)
		}
		return im, nil
	}
	return toFloat(raw, order, img.SampleFormat, w, h, c, lo, hi)
}

func codeMapping(img xisfImage) (step, zero float64, empty int, ok bool) {
	if img.SampleFormat != "UInt16" {
		return 0, 0, 0, false
	}
	vals := map[string]float64{}
	for _, p := range img.Properties {
		if v, err := strconv.ParseFloat(p.Value, 64); err == nil {
			vals[p.ID] = v
		}
	}
	step, okStep := vals[PropCodeStep]
	zero, okZero := vals[PropCodeZero]
	if !okStep || !okZero || !(step > 0) {
		return 0, 0, 0, false
	}
	empty = -1
	if e, okEmpty := vals[PropEmptyCode]; okEmpty {
		empty = int(e)
	}
	return step, zero, empty, true
}

func parseGeometry(g string) (w, h, c int, err error) {
	parts := strings.Split(g, ":")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("%w: geometry %q (only 2-D images)", ErrUnsupported, g)
	}
	var v [3]int
	for i, p := range parts {
		if v[i], err = strconv.Atoi(p); err != nil || v[i] <= 0 {
			return 0, 0, 0, fmt.Errorf("%w: geometry %q", ErrUnsupported, g)
		}
	}
	return v[0], v[1], v[2], nil
}

func parseAttachment(loc string) (pos, size int, err error) {
	parts := strings.Split(loc, ":")
	if len(parts) != 3 || parts[0] != "attachment" {
		return 0, 0, fmt.Errorf("%w: location %q", ErrUnsupported, loc)
	}
	if pos, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, fmt.Errorf("%w: location %q", ErrUnsupported, loc)
	}
	if size, err = strconv.Atoi(parts[2]); err != nil {
		return 0, 0, fmt.Errorf("%w: location %q", ErrUnsupported, loc)
	}
	return pos, size, nil
}

func parseBounds(s string) (float64, float64, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	l, err1 := strconv.ParseFloat(parts[0], 64)
	u, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || u <= l {
		return 0, 0, false
	}
	return l, u, true
}

func sampleBytes(format string) (int, error) {
	switch format {
	case "UInt8":
		return 1, nil
	case "UInt16":
		return 2, nil
	case "UInt32", "Float32":
		return 4, nil
	case "Float64":
		return 8, nil
	default:
		return 0, fmt.Errorf("%w: sample format %q", ErrUnsupported, format)
	}
}

// decompress handles compression="codec[+sh]:uncompressed-size[:item-size]",
// optionally split into subblocks="csize,usize:csize,usize...".
func decompress(data []byte, spec, subblocks string, want int) ([]byte, error) {
	parts := strings.Split(spec, ":")
	codec := parts[0]
	shuffle := strings.HasSuffix(codec, "+sh")
	codec = strings.TrimSuffix(codec, "+sh")
	itemSize := 1
	if len(parts) >= 3 {
		if v, err := strconv.Atoi(parts[2]); err == nil && v > 0 {
			itemSize = v
		}
	}

	type block struct{ c, u int }
	var blocks []block
	if subblocks != "" {
		for _, sb := range strings.Split(subblocks, ":") {
			cu := strings.Split(sb, ",")
			if len(cu) != 2 {
				return nil, fmt.Errorf("%w: subblocks %q", ErrUnsupported, subblocks)
			}
			cs, err1 := strconv.Atoi(cu[0])
			us, err2 := strconv.Atoi(cu[1])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("%w: subblocks %q", ErrUnsupported, subblocks)
			}
			blocks = append(blocks, block{cs, us})
		}
	} else {
		blocks = []block{{len(data), want}}
	}

	out := make([]byte, 0, want)
	off := 0
	for _, bl := range blocks {
		if off+bl.c > len(data) {
			return nil, fmt.Errorf("%w: subblock past end of data", ErrUnsupported)
		}
		src := data[off : off+bl.c]
		off += bl.c
		dst, err := decompressBlock(codec, src, bl.u)
		if err != nil {
			return nil, err
		}
		out = append(out, dst...)
	}
	if shuffle && itemSize > 1 {
		out = unshuffle(out, itemSize)
	}
	return out, nil
}

func decompressBlock(codec string, src []byte, usize int) ([]byte, error) {
	switch codec {
	case "lz4", "lz4hc":
		dst := make([]byte, usize)
		n, err := lz4.UncompressBlock(src, dst)
		if err != nil {
			return nil, fmt.Errorf("lz4: %w", err)
		}
		return dst[:n], nil
	case "zlib":
		r, err := zlib.NewReader(bytes.NewReader(src))
		if err != nil {
			return nil, fmt.Errorf("zlib: %w", err)
		}
		defer r.Close()
		dst := make([]byte, 0, usize)
		buf := bytes.NewBuffer(dst)
		if _, err := io.Copy(buf, io.LimitReader(r, int64(usize)+1)); err != nil {
			return nil, fmt.Errorf("zlib: %w", err)
		}
		return buf.Bytes(), nil
	case "zstd":
		d, err := zstd.NewReader(nil)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		dst, err := d.DecodeAll(src, make([]byte, 0, usize))
		if err != nil {
			return nil, fmt.Errorf("zstd: %w", err)
		}
		return dst, nil
	default:
		return nil, fmt.Errorf("%w: codec %q", ErrUnsupported, codec)
	}
}

// unshuffle reverses XISF byte shuffling: the shuffled block stores byte 0
// of every item, then byte 1 of every item, and so on. Trailing bytes that
// don't fill an item are stored as-is.
func unshuffle(b []byte, itemSize int) []byte {
	n := len(b) / itemSize
	out := make([]byte, len(b))
	for j := range itemSize {
		plane := b[j*n : (j+1)*n]
		for i, v := range plane {
			out[i*itemSize+j] = v
		}
	}
	copy(out[n*itemSize:], b[n*itemSize:])
	return out
}

func toFloat(raw []byte, order binary.ByteOrder, format string, w, h, c int, lo, hi float64) (*Image, error) {
	count := w * h * c
	im := &Image{W: w, H: h, C: c, Data: make([]float32, count)}
	switch format {
	case "UInt8":
		for i := range count {
			im.Data[i] = float32(raw[i]) / math.MaxUint8
		}
	case "UInt16":
		for i := range count {
			im.Data[i] = float32(order.Uint16(raw[2*i:])) / math.MaxUint16
		}
	case "UInt32":
		for i := range count {
			im.Data[i] = float32(float64(order.Uint32(raw[4*i:])) / math.MaxUint32)
		}
	case "Float32":
		scale := float32(hi - lo)
		for i := range count {
			im.Data[i] = (math.Float32frombits(order.Uint32(raw[4*i:])) - float32(lo)) / scale
		}
	case "Float64":
		for i := range count {
			im.Data[i] = float32((math.Float64frombits(order.Uint64(raw[8*i:])) - lo) / (hi - lo))
		}
	default:
		return nil, fmt.Errorf("%w: sample format %q", ErrUnsupported, format)
	}
	return im, nil
}
