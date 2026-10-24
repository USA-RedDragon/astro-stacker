package morningreport

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/klauspost/compress/zstd"
)

// fixture reads NINA lines Loki returned, one JSON object per line with the
// entry's time and text, from testdata; a .zst file is zstd compressed.
func fixture(t *testing.T, name string) []Line {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(name, ".zst") {
		z, err := zstd.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		defer z.Close()
		r = z
	}
	var lines []Line
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var row struct {
			T    time.Time `json:"t"`
			Line string    `json:"line"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, Line{T: row.T.UTC(), Text: row.Line})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func central() *time.Location {
	l, err := time.LoadLocation("America/Chicago")
	if err != nil {
		panic(err)
	}
	return l
}

// ct is a time on the night of 2026-10-23, US Central daylight time; an
// hour before noon is the next morning.
func ct(clock string) time.Time {
	day := "2026-10-23"
	if clock < "12" {
		day = "2026-10-24"
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", day+" "+clock, central())
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// near reports whether a and b are within a millisecond; Loki keeps some
// times a nanosecond off the log's own.
func near(a, b time.Time) bool {
	d := a.Sub(b)
	return d < time.Millisecond && d > -time.Millisecond
}

func night1023() (start, end time.Time) {
	return Schedule{Loc: central(), Hour: 11}.Window(time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
}

// testDark is the made-up test site's dark spans in a window.
func testDark(start, end time.Time) []Span {
	return DarkSpans(sky.Site{Latitude: 32, Longitude: -97}, start, end)
}
