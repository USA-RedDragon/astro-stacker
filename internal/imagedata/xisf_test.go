package imagedata_test

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

// build makes a monolithic XISF with one attached block.
func build(t *testing.T, geometry, format, compression, extra string, block []byte) []byte {
	t.Helper()
	// The header length depends on the attachment offset, which depends on
	// the header length; pad the offset to a fixed 4096 to break the cycle.
	const dataOff = 4096
	comp := ""
	if compression != "" {
		comp = fmt.Sprintf(` compression="%s"`, compression)
	}
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><xisf version="1.0" xmlns="http://www.pixinsight.com/xisf">`+
		`<Image geometry="%s" sampleFormat="%s" colorSpace="Gray" location="attachment:%d:%d"%s%s/>`+
		`<Image geometry="1:1:1" sampleFormat="UInt8" location="attachment:0:1"/></xisf>`,
		geometry, format, dataOff, len(block), comp, extra)
	b := make([]byte, dataOff, dataOff+len(block))
	copy(b, "XISF0100")
	binary.LittleEndian.PutUint32(b[8:12], uint32(len(xml)))
	copy(b[16:], xml)
	return append(b, block...)
}

func u16(vals ...uint16) []byte {
	b := make([]byte, 2*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint16(b[2*i:], v)
	}
	return b
}

func shuffle(b []byte, item int) []byte {
	n := len(b) / item
	out := make([]byte, len(b))
	for i := range n {
		for j := range item {
			out[j*n+i] = b[i*item+j]
		}
	}
	return out
}

var pixels = []uint16{0, 1000, 32768, 65535, 12345, 54321}

func check(t *testing.T, im *imagedata.Image) {
	t.Helper()
	if im.W != 3 || im.H != 2 || im.C != 1 {
		t.Fatalf("geometry %dx%dx%d", im.W, im.H, im.C)
	}
	for i, v := range pixels {
		if want := float32(v) / 65535; math.Abs(float64(im.Data[i]-want)) > 1e-7 {
			t.Errorf("pixel %d = %v want %v", i, im.Data[i], want)
		}
	}
}

func TestUncompressed(t *testing.T) {
	t.Parallel()
	im, err := imagedata.Decode(build(t, "3:2:1", "UInt16", "", "", u16(pixels...)))
	if err != nil {
		t.Fatal(err)
	}
	check(t, im)
}

func TestLZ4HCWithShuffle(t *testing.T) {
	t.Parallel()
	raw := shuffle(u16(pixels...), 2)
	dst := make([]byte, lz4.CompressBlockBound(len(raw)))
	var c lz4.CompressorHC
	n, err := c.CompressBlock(raw, dst)
	if err != nil || n == 0 {
		// Tiny inputs can be incompressible; LZ4 still encodes them as literals.
		t.Fatalf("compress: n=%d err=%v", n, err)
	}
	b := build(t, "3:2:1", "UInt16", fmt.Sprintf("lz4hc+sh:%d:2", len(raw)), "", dst[:n])
	im, err := imagedata.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	check(t, im)
}

func TestZlibSubblocks(t *testing.T) {
	t.Parallel()
	raw := u16(pixels...)
	var blocks []byte
	var sub string
	for i, part := range [][]byte{raw[:4], raw[4:]} {
		var z bytes.Buffer
		w := zlib.NewWriter(&z)
		_, _ = w.Write(part)
		_ = w.Close()
		blocks = append(blocks, z.Bytes()...)
		if i > 0 {
			sub += ":"
		}
		sub += fmt.Sprintf("%d,%d", z.Len(), len(part))
	}
	b := build(t, "3:2:1", "UInt16", fmt.Sprintf("zlib:%d", len(raw)), fmt.Sprintf(` subblocks="%s"`, sub), blocks)
	im, err := imagedata.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	check(t, im)
}

func TestZstdFloat32Bounds(t *testing.T) {
	t.Parallel()
	vals := []float32{0, 0.25, 0.5, 1, 0.75, 0.125}
	raw := make([]byte, 4*len(vals))
	for i, v := range vals {
		// Stored in [0, 2] with bounds 0:2, so decoding must halve them.
		binary.LittleEndian.PutUint32(raw[4*i:], math.Float32bits(v*2))
	}
	enc, _ := zstd.NewWriter(nil)
	block := enc.EncodeAll(shuffle(raw, 4), nil)
	b := build(t, "3:2:1", "Float32", fmt.Sprintf("zstd+sh:%d:4", len(raw)), ` bounds="0:2"`, block)
	im, err := imagedata.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vals {
		if math.Abs(float64(im.Data[i]-v)) > 1e-6 {
			t.Errorf("pixel %d = %v want %v", i, im.Data[i], v)
		}
	}
}

func TestFITSUnsigned16(t *testing.T) {
	t.Parallel()
	cards := []string{"SIMPLE  =                    T", "BITPIX  =                   16", "NAXIS   =                    2",
		"NAXIS1  =                    3", "NAXIS2  =                    2", "BZERO   =                32768", "BSCALE  =                    1", "END"}
	var hdr bytes.Buffer
	for _, c := range cards {
		fmt.Fprintf(&hdr, "%-80s", c)
	}
	for hdr.Len()%2880 != 0 {
		hdr.WriteByte(' ')
	}
	// FITS stores the bottom row first: write row 1, then row 0.
	for _, row := range [][]uint16{pixels[3:], pixels[:3]} {
		for _, v := range row {
			_ = binary.Write(&hdr, binary.BigEndian, int16(int32(v)-32768))
		}
	}
	im, err := imagedata.Decode(hdr.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	check(t, im)
}

// TestRealFile decodes a file named by IMAGEDATA_FILE, the quickest way to
// check a new format. With IMAGEDATA_PREVIEW set to a path, it also writes the
// rendered preview there.
func TestRealFile(t *testing.T) {
	path := os.Getenv("IMAGEDATA_FILE")
	if path == "" {
		t.Skip("set IMAGEDATA_FILE to decode a real frame")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, v := range im.Plane(0) {
		sum += float64(v)
	}
	t.Logf("%dx%dx%d, channel 0 mean %.6f (×65535 = %.3f)", im.W, im.H, im.C, sum/float64(im.W*im.H), 65535*sum/float64(im.W*im.H))

	if out := os.Getenv("IMAGEDATA_PREVIEW"); out != "" {
		jpg, err := preview.Render(im, preview.DefaultOptions)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, jpg, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", out, len(jpg))
	}
}

func TestWriteFITSRoundTrip(t *testing.T) {
	t.Parallel()
	data := []float32{0, 0.25, 0.5, 1, 0.75, 0.125, 0.3, 0.6, 0.9, 0.1, 0.2, 0.4}
	var buf bytes.Buffer
	cards := []imagedata.Card{
		imagedata.StringCard("OBJECT", "Bode's Galaxy", "target"),
		imagedata.FloatCard("EXPTIME", 600, "seconds"),
		imagedata.IntCard("NCOMBINE", 42, "subs"),
		// Copied from a file that said otherwise: the writer's own wins.
		imagedata.StringCard("ROWORDER", "TOP-DOWN", ""),
	}
	if err := imagedata.WriteFITS(&buf, 3, 2, 2, data, cards); err != nil {
		t.Fatal(err)
	}
	if buf.Len()%2880 != 0 {
		t.Errorf("file is %d bytes, not whole blocks", buf.Len())
	}
	im, err := imagedata.Decode(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if im.W != 3 || im.H != 2 || im.C != 2 {
		t.Fatalf("geometry %dx%dx%d", im.W, im.H, im.C)
	}
	for i, v := range data {
		if im.Data[i] != v {
			t.Errorf("sample %d = %v want %v", i, im.Data[i], v)
		}
	}
	// On disk the bottom row comes first: the first sample is data[3] (row 1).
	off := bytes.Index(buf.Bytes(), []byte(fmt.Sprintf("%-80s", "END"))) + 80
	off = (off + 2879) / 2880 * 2880
	if got := math.Float32frombits(binary.BigEndian.Uint32(buf.Bytes()[off:])); got != data[3] {
		t.Errorf("first sample on disk = %v, want the bottom row's %v", got, data[3])
	}
	if !bytes.Contains(buf.Bytes(), []byte("OBJECT  = 'Bode''s Galaxy'")) {
		t.Error("quoted string card not written as expected")
	}
	// PixInsight reads FITS top row first unless ROWORDER says otherwise.
	if n := bytes.Count(buf.Bytes(), []byte("ROWORDER=")); n != 1 || !bytes.Contains(buf.Bytes(), []byte("ROWORDER= 'BOTTOM-UP'")) {
		t.Errorf("want one ROWORDER= 'BOTTOM-UP', have %d ROWORDER cards", n)
	}
}
