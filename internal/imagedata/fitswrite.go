package imagedata

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
)

// Card is one FITS header keyword. Value is written as-is for numbers and
// booleans; set Str to write a quoted string.
type Card struct {
	Key     string
	Value   string
	Str     bool
	Comment string
}

// StringCard and FloatCard build header cards.
func StringCard(key, value, comment string) Card {
	return Card{Key: key, Value: value, Str: true, Comment: comment}
}

func FloatCard(key string, value float64, comment string) Card {
	return Card{Key: key, Value: fmt.Sprintf("%.10g", value), Comment: comment}
}

func IntCard(key string, value int, comment string) Card {
	return Card{Key: key, Value: fmt.Sprintf("%d", value), Comment: comment}
}

// WriteFITS writes a 32-bit float FITS image. data is planar
// (c*w*h + y*w + x) with row 0 at the top, which FITS stores as NAXIS1=w,
// NAXIS2=h, NAXIS3=c with the bottom row first, as Siril and PixInsight do.
func WriteFITS(out io.Writer, w, h, c int, data []float32, extra []Card) error {
	if len(data) != w*h*c {
		return fmt.Errorf("have %d samples, want %d", len(data), w*h*c)
	}
	bw := bufio.NewWriterSize(out, 1<<20)
	cards := []Card{
		{Key: "SIMPLE", Value: "T"},
		{Key: "BITPIX", Value: "-32"},
		IntCard("NAXIS", map[bool]int{true: 3, false: 2}[c > 1], ""),
		IntCard("NAXIS1", w, ""),
		IntCard("NAXIS2", h, ""),
	}
	if c > 1 {
		cards = append(cards, IntCard("NAXIS3", c, ""))
	}
	cards = append(cards, extra...)
	written := 0
	for _, cd := range cards {
		if _, err := bw.WriteString(formatCard(cd)); err != nil {
			return err
		}
		written += 80
	}
	if _, err := fmt.Fprintf(bw, "%-80s", "END"); err != nil {
		return err
	}
	written += 80
	if err := pad(bw, written, ' '); err != nil {
		return err
	}
	buf := make([]byte, 4)
	for p := range c {
		plane := data[p*w*h : (p+1)*w*h]
		for y := h - 1; y >= 0; y-- {
			for _, v := range plane[y*w : (y+1)*w] {
				binary.BigEndian.PutUint32(buf, math.Float32bits(v))
				if _, err := bw.Write(buf); err != nil {
					return err
				}
			}
		}
	}
	if err := pad(bw, 4*len(data), 0); err != nil {
		return err
	}
	return bw.Flush()
}

func formatCard(c Card) string {
	v := c.Value
	if c.Str {
		v = "'" + strings.ReplaceAll(c.Value, "'", "''") + "'"
		v = fmt.Sprintf("%-8s", v)
	} else {
		v = fmt.Sprintf("%20s", v)
	}
	s := fmt.Sprintf("%-8s= %s", c.Key, v)
	if c.Comment != "" {
		s += " / " + c.Comment
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return fmt.Sprintf("%-80s", s)
}

func pad(w *bufio.Writer, n int, b byte) error {
	if r := n % fitsBlock; r != 0 {
		_, err := w.WriteString(strings.Repeat(string(b), fitsBlock-r))
		return err
	}
	return nil
}
