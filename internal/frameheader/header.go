// Package frameheader reads acquisition metadata from the start of an XISF
// or FITS file, so frames can be indexed with a ranged read instead of a
// full download.
package frameheader

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// PrefixSize is enough to hold a NINA XISF header (about 10 KB) or a FITS
// header of a few blocks. Parse reports when it needs more.
const PrefixSize = 16 * 1024

const (
	xisfMagic     = "XISF0100"
	fitsBlockSize = 2880
	fitsCardSize  = 80
)

var ErrUnknownFormat = errors.New("not an XISF or FITS file")

// NeedMoreError means the header extends past the bytes provided.
type NeedMoreError struct{ Total int }

func (e *NeedMoreError) Error() string {
	return fmt.Sprintf("header needs %d bytes", e.Total)
}

// Keywords holds FITS keywords with string values unquoted and trimmed.
type Keywords map[string]string

// Parse extracts the FITS keywords from an XISF or FITS file prefix.
func Parse(prefix []byte) (Keywords, error) {
	cards, props, err := parse(prefix)
	if err != nil {
		return nil, err
	}
	kw := Keywords{}
	for _, c := range cards {
		if _, dup := kw[c.Name]; !dup {
			kw[c.Name] = c.Value
		}
	}
	return merge(kw, props), nil
}

// Card is one FITS keyword in header order. Value is unquoted; Quoted says
// it was a string.
type Card struct {
	Name, Value, Comment string
	Quoted               bool
}

// ParseCards returns an XISF or FITS file prefix's FITS keywords in order,
// with their comments, for copying into another file's header.
func ParseCards(prefix []byte) ([]Card, error) {
	cards, _, err := parse(prefix)
	return cards, err
}

func parse(prefix []byte) ([]Card, Keywords, error) {
	switch {
	case bytes.HasPrefix(prefix, []byte(xisfMagic)):
		return parseXISF(prefix)
	case bytes.HasPrefix(prefix, []byte("SIMPLE  =")):
		cards, err := parseFITS(prefix)
		return cards, nil, err
	default:
		return nil, nil, ErrUnknownFormat
	}
}

func newCard(name, raw, comment string) Card {
	raw = strings.TrimSpace(raw)
	return Card{Name: name, Value: cleanValue(raw), Comment: strings.TrimSpace(comment), Quoted: strings.HasPrefix(raw, "'")}
}

// xisfProperties maps XISF property ids to the FITS keyword they stand in
// for, used only when the FITS keyword itself is absent.
func xisfProperties() map[string]string {
	return map[string]string{
		"Instrument:ExposureTime":        "EXPTIME",
		"Observation:Time:Start":         "DATE-OBS",
		"Instrument:Filter:Name":         "FILTER",
		"Instrument:Sensor:Temperature":  "CCD-TEMP",
		"Instrument:Camera:Gain":         "GAIN",
		"Instrument:Camera:XBinning":     "XBINNING",
		"Instrument:Camera:YBinning":     "YBINNING",
		"Instrument:Camera:Name":         "INSTRUME",
		"Observation:Object:Name":        "OBJECT",
		"Observation:Location:Longitude": "SITELONG",
		"Instrument:Sensor:XPixelSize":   "XPIXSZ",
		"Instrument:Sensor:YPixelSize":   "YPIXSZ",
		"Instrument:Telescope:Name":      "TELESCOP",
	}
}

// parseXISF reads the XML header of a monolithic XISF file: an 8-byte
// signature, a little-endian uint32 header length, 4 reserved bytes, then the
// XML. Only the first Image element counts, since masters also carry
// rejection maps as extra images with their own keywords.
func parseXISF(b []byte) ([]Card, Keywords, error) {
	if len(b) < 16 {
		return nil, nil, &NeedMoreError{Total: 16}
	}
	n := int(binary.LittleEndian.Uint32(b[8:12]))
	if len(b) < 16+n {
		return nil, nil, &NeedMoreError{Total: 16 + n}
	}
	// The header may be padded with NULs after the XML.
	header := bytes.TrimRight(b[16:16+n], "\x00")

	var cards []Card
	props := Keywords{}
	properties := xisfProperties()
	dec := xml.NewDecoder(bytes.NewReader(header))
	depth := 0 // nesting inside the first Image; 0 means not inside it
	seenImage := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("xisf header: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if t.Name.Local == "Image" && !seenImage {
					seenImage = true
					depth = 1
					geometryProps(attr(t, "geometry"), props)
				}
				continue
			}
			depth++
			switch t.Name.Local {
			case "FITSKeyword":
				if name := attr(t, "name"); name != "" {
					cards = append(cards, newCard(name, attr(t, "value"), attr(t, "comment")))
				}
			case "Property":
				id := attr(t, "id")
				value, hasValue := attrOK(t, "value")
				if !hasValue {
					// String properties may carry their value as element text.
					var text string
					if err := dec.DecodeElement(&text, &t); err == nil {
						value = text
					}
					depth--
				}
				if key, ok := properties[id]; ok {
					props[key] = strings.TrimSpace(value)
				}
			}
		case xml.EndElement:
			if depth > 0 {
				depth--
				if depth == 0 {
					// Finished the first image; later images are ignored.
					return cards, props, nil
				}
			}
		}
	}
	return cards, props, nil
}

func geometryProps(geometry string, props Keywords) {
	parts := strings.Split(geometry, ":")
	if len(parts) < 2 {
		return
	}
	for i, k := range []string{"NAXIS1", "NAXIS2"} {
		if n, err := strconv.Atoi(strings.TrimSpace(parts[i])); err == nil && n > 0 {
			props[k] = strconv.Itoa(n)
		}
	}
}

func attr(t xml.StartElement, name string) string {
	v, _ := attrOK(t, name)
	return v
}

func attrOK(t xml.StartElement, name string) (string, bool) {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

// merge fills keywords missing from kw with the XISF property equivalents.
func merge(kw, props Keywords) Keywords {
	for k, v := range props {
		if _, ok := kw[k]; !ok && v != "" {
			kw[k] = v
		}
	}
	return kw
}

func parseFITS(b []byte) ([]Card, error) {
	var cards []Card
	for off := 0; ; off += fitsCardSize {
		if off+fitsCardSize > len(b) {
			// Headers are padded to whole blocks, so ask for the next one.
			return nil, &NeedMoreError{Total: (off/fitsBlockSize + 1) * fitsBlockSize}
		}
		card := string(b[off : off+fitsCardSize])
		key := strings.TrimSpace(card[:8])
		if key == "END" {
			return cards, nil
		}
		if len(card) > 10 && card[8:10] == "= " {
			value := stripComment(card[10:])
			comment := strings.TrimPrefix(card[10+len(value):], "/")
			cards = append(cards, newCard(key, value, comment))
		}
	}
}

// stripComment drops a trailing "/ comment" that is outside a quoted string.
func stripComment(v string) string {
	inQuote := false
	for i, r := range v {
		switch {
		case r == '\'':
			inQuote = !inQuote
		case r == '/' && !inQuote:
			return v[:i]
		}
	}
	return v
}

func cleanValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		v = strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return strings.TrimSpace(v)
}

// String returns a keyword's value, or "" if absent.
func (k Keywords) String(name string) string { return k[name] }

// Float returns a keyword as a float, or NaN if absent or unparseable.
func (k Keywords) Float(name string) float64 {
	v, ok := k[name]
	if !ok {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// Time parses a FITS date keyword such as DATE-OBS, which NINA writes in UTC
// without a zone designator.
func (k Keywords) Time(name string) (time.Time, bool) {
	v, ok := k[name]
	if !ok {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
