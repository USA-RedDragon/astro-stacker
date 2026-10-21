package claimswatch

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Alerts reads Gaia's science alerts list and every ATel published since
// the last check, and matches them to the stars by position, Gaia DR3 id or
// 2MASS name. ATels have no position search, so each one is read once; the
// first check reads back to Since.
type Alerts struct {
	Client   *http.Client
	GaiaURL  string
	ATelURL  string
	Since    time.Time
	Radius   float64
	MaxATels int
}

const (
	gaiaAlertsCSV = "https://gsaweb.ast.cam.ac.uk/alerts/alerts.csv"
	atelBase      = "https://www.astronomerstelegram.org/"
	stateATelLast = "atel:last"
)

func (a *Alerts) Where() string { return WhereAlerts }
func (a *Alerts) Off() string   { return "" }

func (a *Alerts) radius() float64 {
	if a.Radius > 0 {
		return a.Radius
	}
	return 10
}

func (a *Alerts) Check(ctx context.Context, stars []Star, st State) (Outcome, error) {
	out := newOutcome()
	gu := a.GaiaURL
	if gu == "" {
		gu = gaiaAlertsCSV
	}
	b, err := get(ctx, a.Client, gu, 64<<20)
	var rows []GaiaAlert
	if err == nil {
		rows, err = ParseGaiaAlerts(b)
	}
	if err != nil {
		return out, fmt.Errorf("gaia alerts: %w", err)
	}
	for _, s := range stars {
		for _, r := range rows {
			if sep := SepArcsec(s.RA, s.Dec, r.RA, r.Dec); sep <= a.radius() {
				out.Found[s.SourceID] = append(out.Found[s.SourceID], r.mention(sep))
			}
		}
	}
	if err := a.atels(ctx, stars, st, out); err != nil {
		return out, err
	}
	return out, nil
}

// GaiaAlert is a row of Gaia's alerts list.
type GaiaAlert struct {
	Name, Class, Comment string
	RA, Dec              float64
	Published            *time.Time
}

func (g GaiaAlert) mention(sep float64) Mention {
	d := fmt.Sprintf(`Gaia alert, %.1f" from your star`, sep)
	if g.Class != "" && g.Class != "unknown" {
		d += ", classed " + g.Class
	}
	if g.Comment != "" {
		d += ": " + g.Comment
	}
	return Mention{Key: "gaia:" + g.Name, Title: g.Name, Detail: d + ".", Dated: g.Published,
		URL: "https://gsaweb.ast.cam.ac.uk/alerts/alert/" + g.Name + "/"}
}

// ParseGaiaAlerts reads alerts.csv.
func ParseGaiaAlerts(b []byte) ([]GaiaAlert, error) {
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 || !strings.HasPrefix(recs[0][0], "#Name") {
		return nil, errors.New("unexpected alerts list")
	}
	var out []GaiaAlert
	for _, f := range recs[1:] {
		if len(f) < 10 {
			continue
		}
		ra, err1 := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
		dec, err2 := strconv.ParseFloat(strings.TrimSpace(f[3]), 64)
		if err1 != nil || err2 != nil {
			continue
		}
		g := GaiaAlert{Name: strings.TrimSpace(f[0]), RA: ra, Dec: dec, Class: strings.TrimSpace(f[7]), Comment: strings.TrimSpace(f[9])}
		if t, err := time.Parse(time.DateTime, strings.TrimSpace(f[8])); err == nil {
			g.Published = day(t)
		}
		out = append(out, g)
	}
	return out, nil
}

var (
	atelRSSItem = regexp.MustCompile(`\?read=(\d+)`)
	atelDate    = regexp.MustCompile(`on\s+(\d{1,2} [A-Z][a-z]{2} \d{4}); (\d{2}:\d{2}) UT`)
	atelTitle   = regexp.MustCompile(`(?s)<title>\s*(.*?)\s*</title>`)
	tagRE       = regexp.MustCompile(`(?s)<[^>]*>`)
	sexaRE      = regexp.MustCompile(`(\d{1,2})[:h ]\s?(\d{2})[:m ]\s?(\d{2}(?:\.\d+)?)s?\s*[,;|]?\s*([+\-\x{2212}])\s?(\d{1,2})[:d° ]\s?(\d{2})[:'′m ]\s?(\d{2}(?:\.\d+)?)`)
	decimalRE   = regexp.MustCompile(`(\d{1,3}\.\d{3,})\s*[, ]\s*([+\-]?\d{1,2}\.\d{3,})`)
)

// NewestATel is the highest telegram number in the ATel feed.
func NewestATel(rss []byte) int {
	newest := 0
	for _, m := range atelRSSItem.FindAllSubmatch(rss, -1) {
		if n, _ := strconv.Atoi(string(m[1])); n > newest {
			newest = n
		}
	}
	return newest
}

// ATel is one telegram's number, title, date and plain text.
type ATel struct {
	Number int
	Title  string
	Date   *time.Time
	Text   string
}

// ParseATel reads a telegram's page.
func ParseATel(n int, page []byte) ATel {
	a := ATel{Number: n}
	if m := atelTitle.FindSubmatch(page); m != nil {
		a.Title = strings.TrimSpace(html.UnescapeString(string(m[1])))
	}
	text := html.UnescapeString(tagRE.ReplaceAllString(string(page), " "))
	a.Text = strings.Join(strings.Fields(text), " ")
	if m := atelDate.FindStringSubmatch(a.Text); m != nil {
		if t, err := time.Parse("2 Jan 2006 15:04", m[1]+" "+m[2]); err == nil {
			a.Date = day(t)
		}
	}
	return a
}

// Positions are the coordinates written in a text, in degrees.
func Positions(text string) [][2]float64 {
	var out [][2]float64
	for _, m := range sexaRE.FindAllStringSubmatch(text, -1) {
		h, _ := strconv.ParseFloat(m[1], 64)
		mi, _ := strconv.ParseFloat(m[2], 64)
		s, _ := strconv.ParseFloat(m[3], 64)
		d, _ := strconv.ParseFloat(m[5], 64)
		dm, _ := strconv.ParseFloat(m[6], 64)
		ds, _ := strconv.ParseFloat(m[7], 64)
		if h >= 24 || mi >= 60 || s >= 60 || d > 90 || dm >= 60 || ds >= 60 {
			continue
		}
		dec := d + dm/60 + ds/3600
		if m[4] != "+" {
			dec = -dec
		}
		out = append(out, [2]float64{(h + mi/60 + s/3600) * 15, dec})
	}
	for _, m := range decimalRE.FindAllStringSubmatch(text, -1) {
		ra, _ := strconv.ParseFloat(m[1], 64)
		dec, _ := strconv.ParseFloat(m[2], 64)
		if ra < 360 && dec >= -90 && dec <= 90 {
			out = append(out, [2]float64{ra, dec})
		}
	}
	return out
}

// Match tells whether a telegram names the star or gives a position within
// radius arcseconds of it, and how.
func (a ATel) Match(s Star, radius float64) (string, bool) {
	if s.GaiaID != 0 && strings.Contains(a.Text, strconv.FormatInt(s.GaiaID, 10)) {
		return "names its Gaia DR3 id", true
	}
	if j := strings.TrimSpace(strings.TrimPrefix(s.TwoMASS, "2MASS")); j != "" && strings.Contains(a.Text, j) {
		return "names it as 2MASS " + j, true
	}
	for _, p := range Positions(a.Text) {
		if sep := SepArcsec(s.RA, s.Dec, p[0], p[1]); sep <= radius {
			return fmt.Sprintf(`gives a position %.1f" from your star`, sep), true
		}
	}
	return "", false
}

func (a *Alerts) atels(ctx context.Context, stars []Star, st State, out Outcome) error {
	base := a.ATelURL
	if base == "" {
		base = atelBase
	}
	rss, err := get(ctx, a.Client, base+"?rss", 4<<20)
	if err != nil {
		return fmt.Errorf("atel: %w", err)
	}
	newest := NewestATel(rss)
	if newest == 0 {
		return errors.New("atel: no telegrams in the feed")
	}
	maxN := a.MaxATels
	if maxN <= 0 {
		maxN = 600
	}
	last := 0
	if v, err := st.Get(ctx, stateATelLast); err == nil {
		last, _ = strconv.Atoi(v)
	}
	read := func(n int) (ATel, error) {
		page, err := get(ctx, a.Client, fmt.Sprintf("%s?read=%d", base, n), 4<<20)
		if err != nil {
			return ATel{}, err
		}
		return ParseATel(n, page), nil
	}
	look := func(t ATel) {
		for _, s := range stars {
			if how, ok := t.Match(s, a.radius()); ok {
				out.Found[s.SourceID] = append(out.Found[s.SourceID], Mention{Key: fmt.Sprintf("atel:%d", t.Number),
					Title: fmt.Sprintf("ATel #%d", t.Number), Detail: strings.TrimSpace(strings.TrimPrefix(t.Title, fmt.Sprintf("ATel #%d:", t.Number))) + "; it " + how + ".",
					URL: fmt.Sprintf("%s?read=%d", base, t.Number), Dated: t.Date})
			}
		}
	}
	if last == 0 {
		for n, i := newest, 0; n > 0 && i < maxN; n, i = n-1, i+1 {
			t, err := read(n)
			if err != nil {
				return fmt.Errorf("atel %d: %w", n, err)
			}
			if t.Date != nil && t.Date.Before(a.Since) {
				break
			}
			look(t)
		}
		return st.Set(ctx, stateATelLast, strconv.Itoa(newest))
	}
	done := last
	for n := last + 1; n <= newest && n <= last+maxN; n++ {
		t, err := read(n)
		if err != nil {
			return fmt.Errorf("atel %d: %w", n, err)
		}
		look(t)
		done = n
	}
	return st.Set(ctx, stateATelLast, strconv.Itoa(done))
}
