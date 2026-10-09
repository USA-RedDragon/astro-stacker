package imagedata

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Property is an XISF image property. Value is a string, a float64 or a
// []float64; a matrix is a []float64 in row order with Rows set.
type Property struct {
	ID    string
	Value any
	Rows  int
}

// xisfAlign is where the image data starts, as PixInsight aligns it.
const xisfAlign = 4096

// WriteXISF writes a one-channel 32-bit float image as a monolithic XISF
// file, the format PixInsight reads natively. data is row-major with row 0 at
// the top, as XISF stores it. keywords go in as FITSKeyword elements and
// props as Property elements.
func WriteXISF(out io.Writer, w, h int, data []float32, keywords []Card, props []Property) error {
	if len(data) != w*h {
		return fmt.Errorf("have %d samples, want %d", len(data), w*h)
	}
	attrs := `sampleFormat="Float32" bounds="0:1"`
	bw, err := writeXISFHeader(out, w, h, attrs, 4*len(data), keywords, props)
	if err != nil {
		return err
	}
	buf := make([]byte, 4)
	for _, v := range data {
		binary.LittleEndian.PutUint32(buf, math.Float32bits(v))
		if _, err := bw.Write(buf); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func WriteXISF16(out io.Writer, w, h int, codes []uint16, keywords []Card, props []Property) error {
	if len(codes) != w*h {
		return fmt.Errorf("have %d samples, want %d", len(codes), w*h)
	}
	raw := make([]byte, 2*len(codes))
	n := len(codes)
	var two [2]byte
	for i, q := range codes {
		binary.LittleEndian.PutUint16(two[:], q)
		raw[i], raw[n+i] = two[0], two[1]
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return err
	}
	block := enc.EncodeAll(raw, nil)
	enc.Close()
	attrs := fmt.Sprintf(`sampleFormat="UInt16" compression="zstd+sh:%d:2"`, len(raw))
	bw, err := writeXISFHeader(out, w, h, attrs, len(block), keywords, props)
	if err != nil {
		return err
	}
	if _, err := bw.Write(block); err != nil {
		return err
	}
	return bw.Flush()
}

func writeXISFHeader(out io.Writer, w, h int, attrs string, size int, keywords []Card, props []Property) (*bufio.Writer, error) {
	pos := xisfAlign
	var header string
	for range 4 {
		header = xisfHeader(w, h, attrs, pos, size, keywords, props)
		next := (16 + len(header) + xisfAlign - 1) / xisfAlign * xisfAlign
		if next == pos {
			break
		}
		pos = next
	}
	if 16+len(header) > pos {
		return nil, fmt.Errorf("xisf header of %d bytes does not fit before %d", len(header), pos)
	}
	n := len(header)
	if uint64(n) > math.MaxUint32 {
		return nil, fmt.Errorf("xisf header of %d bytes is too long", n)
	}
	bw := bufio.NewWriterSize(out, 1<<20)
	var prefix [16]byte
	copy(prefix[:], "XISF0100")
	binary.LittleEndian.PutUint32(prefix[8:], uint32(n))
	if _, err := bw.Write(prefix[:]); err != nil {
		return nil, err
	}
	if _, err := bw.WriteString(header); err != nil {
		return nil, err
	}
	if _, err := bw.Write(make([]byte, pos-16-len(header))); err != nil {
		return nil, err
	}
	return bw, nil
}

func xisfHeader(w, h int, attrs string, pos, size int, keywords []Card, props []Property) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<xisf version="1.0" xmlns="http://www.pixinsight.com/xisf" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:schemaLocation="http://www.pixinsight.com/xisf http://pixinsight.com/xisf/xisf-1.0.xsd">`)
	fmt.Fprintf(&sb, `<Image geometry="%d:%d:1" %s colorSpace="Gray" location="attachment:%d:%d">`, w, h, attrs, pos, size)
	for _, p := range props {
		switch v := p.Value.(type) {
		case string:
			fmt.Fprintf(&sb, `<Property id="%s" type="String">%s</Property>`, xmlEscape(p.ID), xmlEscape(v))
		case float64:
			fmt.Fprintf(&sb, `<Property id="%s" type="Float64" value="%s"/>`, xmlEscape(p.ID), strconv.FormatFloat(v, 'g', -1, 64))
		case []float64:
			b := make([]byte, 8*len(v))
			for i, f := range v {
				binary.LittleEndian.PutUint64(b[8*i:], math.Float64bits(f))
			}
			enc := base64.StdEncoding.EncodeToString(b)
			if p.Rows > 0 {
				fmt.Fprintf(&sb, `<Property id="%s" type="F64Matrix" rows="%d" columns="%d" location="inline:base64">%s</Property>`,
					xmlEscape(p.ID), p.Rows, len(v)/p.Rows, enc)
			} else {
				fmt.Fprintf(&sb, `<Property id="%s" type="F64Vector" length="%d" location="inline:base64">%s</Property>`,
					xmlEscape(p.ID), len(v), enc)
			}
		}
	}
	for _, c := range keywords {
		v := c.Value
		if c.Str {
			v = "'" + strings.ReplaceAll(v, "'", "''") + "'"
		}
		fmt.Fprintf(&sb, `<FITSKeyword name="%s" value="%s" comment="%s"/>`, xmlEscape(c.Key), xmlEscape(v), xmlEscape(c.Comment))
	}
	sb.WriteString(`</Image><Metadata><Property id="XISF:CreatorApplication" type="String">astro-stacker</Property></Metadata></xisf>`)
	return sb.String()
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}
