package frameheader_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
)

func xisf(xml string) []byte {
	b := make([]byte, 16, 16+len(xml))
	copy(b, "XISF0100")
	binary.LittleEndian.PutUint32(b[8:12], uint32(len(xml)))
	return append(b, xml...)
}

func card(s string) string { return fmt.Sprintf("%-80s", s) }

func fits(cards ...string) []byte {
	var sb strings.Builder
	for _, c := range cards {
		sb.WriteString(card(c))
	}
	sb.WriteString(card("END"))
	for sb.Len()%2880 != 0 {
		sb.WriteByte(' ')
	}
	return []byte(sb.String())
}

// A trimmed copy of a real NINA 3.1 XISF header.
const ninaXML = `<?xml version="1.0" encoding="UTF-8"?><xisf version="1.0"><Image geometry="6248:4176:1" sampleFormat="UInt16">
<FITSKeyword name="IMAGETYP" value="'LIGHT'" comment="Type of exposure"/>
<FITSKeyword name="DATE-OBS" value="'2026-09-28T05:52:17.168'" comment=""/>
<FITSKeyword name="EXPTIME" value="300.0" comment=""/>
<FITSKeyword name="INSTRUME" value="'ZWO ASI2600MM Pro'" comment=""/>
<FITSKeyword name="GAIN" value="0" comment=""/>
<FITSKeyword name="OFFSET" value="50" comment=""/>
<FITSKeyword name="XBINNING" value="1" comment=""/>
<FITSKeyword name="YBINNING" value="1" comment=""/>
<FITSKeyword name="SET-TEMP" value="0.0" comment=""/>
<FITSKeyword name="CCD-TEMP" value="0.1" comment=""/>
<FITSKeyword name="SITELONG" value="-99.382222" comment=""/>
<FITSKeyword name="OBJECT" value="'Cygnis Loop Panel 1'" comment=""/>
<FITSKeyword name="ROTATANG" value="142.399993896484" comment=""/>
<FITSKeyword name="FILTER" value="'Red'" comment=""/>
</Image></xisf>`

func TestParseNINAXISF(t *testing.T) {
	t.Parallel()
	kw, err := frameheader.Parse(xisf(ninaXML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	f := frameheader.FromKeywords(kw)
	if f.Type != "LIGHT" || f.Filter != "Red" || f.Object != "Cygnis Loop Panel 1" || f.Camera != "ZWO ASI2600MM Pro" {
		t.Errorf("strings: %+v", f)
	}
	if f.Exposure != 300 || f.Gain != 0 || f.Offset != 50 || f.SetTemp != 0 || f.CCDTemp != 0.1 || f.BinX != 1 {
		t.Errorf("numbers: %+v", f)
	}
	if math.Abs(f.Rotator-142.4) > 0.001 {
		t.Errorf("rotator = %v", f.Rotator)
	}
	// 05:52 UTC at -99.4 degrees is about 23:14 local solar time on the 27th.
	night, ok := f.Night()
	if !ok || !night.Equal(time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("night = %v, %v", night, ok)
	}
}

func TestParseXISFNeedsMore(t *testing.T) {
	t.Parallel()
	full := xisf(ninaXML)
	_, err := frameheader.Parse(full[:100])
	var more *frameheader.NeedMoreError
	if !errors.As(err, &more) || more.Total != len(full) {
		t.Fatalf("err = %v, want NeedMoreError{%d}", err, len(full))
	}
}

func TestParseFITS(t *testing.T) {
	t.Parallel()
	b := fits(
		"SIMPLE  =                    T",
		"IMAGETYP= 'Dark Frame'         / type",
		"EXPTIME =                600.0 / seconds",
		"FILTER  = 'O''III / narrow'    / quoted slash and escaped quote",
		"DATE-OBS= '2025-02-06T10:00:00'",
	)
	kw, err := frameheader.Parse(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	f := frameheader.FromKeywords(kw)
	if f.Type != "DARK" || f.Exposure != 600 || f.Filter != "O'III / narrow" {
		t.Errorf("got %+v", f)
	}
	if !math.IsNaN(f.Gain) {
		t.Errorf("missing gain should be NaN, got %v", f.Gain)
	}

	_, err = frameheader.Parse(b[:80*3])
	var more *frameheader.NeedMoreError
	if !errors.As(err, &more) || more.Total != 2880 {
		t.Fatalf("truncated FITS: err = %v", err)
	}
}

func TestNormalizeTypes(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"'FLAT'": "FLAT", "'Bias Frame'": "BIAS", "'DARKFLAT'": "DARKFLAT", "'Flat Dark'": "DARKFLAT", "'Light Frame'": "LIGHT", "'Master Flat'": "MASTERFLAT",
	} {
		kw, _ := frameheader.Parse(fits("SIMPLE  = T", "IMAGETYP= "+in))
		if got := frameheader.FromKeywords(kw).Type; got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func TestUnknownFormat(t *testing.T) {
	t.Parallel()
	if _, err := frameheader.Parse([]byte("PNG....")); !errors.Is(err, frameheader.ErrUnknownFormat) {
		t.Errorf("err = %v", err)
	}
}

func TestXISFSpecEdgeCases(t *testing.T) {
	t.Parallel()
	// Default namespace, reversed attribute order, a master with a rejection
	// map as a second image, and properties standing in for missing keywords.
	x := `<?xml version="1.0" encoding="UTF-8"?>
<xisf version="1.0" xmlns="http://www.pixinsight.com/xisf">
<Image id="integration" geometry="10:10:1" sampleFormat="Float32">
<FITSKeyword value="'MASTER FLAT'" name="IMAGETYP" comment=""/>
<FITSKeyword comment="" value="'Red'" name="FILTER"/>
<Property id="Instrument:ExposureTime" type="Float32" value="1.25"/>
<Property id="Observation:Object:Name" type="String">Flat &amp; Field</Property>
<Property id="Instrument:Filter:Name" type="String">ignored, keyword wins</Property>
</Image>
<Image id="rejection_low" geometry="10:10:1" sampleFormat="UInt8">
<FITSKeyword name="IMAGETYP" value="'REJECTION'"/>
<FITSKeyword name="GAIN" value="999"/>
</Image>
</xisf>` + "\x00\x00\x00\x00"
	kw, err := frameheader.Parse(xisf(x))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	f := frameheader.FromKeywords(kw)
	if f.Type != "MASTERFLAT" || f.Filter != "Red" || f.Exposure != 1.25 || f.Object != "Flat & Field" {
		t.Errorf("got %+v", f)
	}
	if !math.IsNaN(f.Gain) {
		t.Errorf("gain from the second image leaked in: %v", f.Gain)
	}
}
