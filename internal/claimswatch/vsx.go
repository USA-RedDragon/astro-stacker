package claimswatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// VSX searches AAVSO's live VSX, not the CDS copy the pipeline mirrors,
// since the copy lags by months. Each entry's first revision date is when
// it was added.
type VSX struct {
	Client *http.Client
	// BaseURL is VSX's index.php; vsx.aavso.org answers scripts, while
	// www.aavso.org sits behind a browser check.
	BaseURL string
	// Discoverer is the user's discoverer name in VSX; their own entries
	// are marked as theirs.
	Discoverer func(ctx context.Context) string
	Radius     float64
}

const vsxBase = "https://vsx.aavso.org/index.php"

func (v *VSX) base() string {
	if v.BaseURL != "" {
		return v.BaseURL
	}
	return vsxBase
}

func (v *VSX) Where() string { return WhereVSX }
func (v *VSX) Off() string   { return "" }

// VSXEntry is a row of VSX's api.list answer.
type VSXEntry struct {
	Name            string `json:"Name"`
	OID             string `json:"OID"`
	RA              string `json:"RA2000"`
	Dec             string `json:"Declination2000"`
	VariabilityType string `json:"VariabilityType"`
	Period          string `json:"Period"`
	MaxMag          string `json:"MaxMag"`
	MinMag          string `json:"MinMag"`
	Discoverer      string `json:"Discoverer"`
}

// ParseVSXList reads api.list's JSON: an empty search is a list, a hit a
// keyed object.
func ParseVSXList(b []byte) ([]VSXEntry, error) {
	var raw struct {
		VSXObjects json.RawMessage `json:"VSXObjects"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("vsx: unexpected answer: %w", err)
	}
	s := strings.TrimSpace(string(raw.VSXObjects))
	if s == "" || strings.HasPrefix(s, "[") {
		return nil, nil
	}
	var objs struct {
		VSXObject []VSXEntry `json:"VSXObject"`
	}
	if err := json.Unmarshal(raw.VSXObjects, &objs); err != nil {
		return nil, fmt.Errorf("vsx: unexpected answer: %w", err)
	}
	return objs.VSXObject, nil
}

var vsxRevision = regexp.MustCompile(`(\d{4}-\d{2}-\d{2} \d{2}:\d{2}) UTC`)

// ParseVSXAdded is the earliest revision date on a VSX detail page: when
// the entry was added.
func ParseVSXAdded(page []byte) *time.Time {
	var first *time.Time
	for _, m := range vsxRevision.FindAllSubmatch(page, -1) {
		t, err := time.Parse("2006-01-02 15:04", string(m[1]))
		if err != nil {
			continue
		}
		if first == nil || t.Before(*first) {
			first = day(t)
		}
	}
	return first
}

func (v *VSX) Check(ctx context.Context, stars []Star, _ State) (Outcome, error) {
	out := newOutcome()
	radius := v.Radius
	if radius <= 0 {
		radius = 10
	}
	mine := ""
	if v.Discoverer != nil {
		mine = strings.TrimSpace(v.Discoverer(ctx))
	}
	fails := 0
	for _, s := range stars {
		q := url.Values{"view": {"api.list"}, "ra": {strconv.FormatFloat(s.RA, 'f', 6, 64)}, "dec": {strconv.FormatFloat(s.Dec, 'f', 6, 64)},
			"radius": {strconv.FormatFloat(radius/3600, 'f', 6, 64)}, "format": {"json"}}
		b, err := get(ctx, v.Client, v.base()+"?"+q.Encode(), 4<<20)
		var rows []VSXEntry
		if err == nil {
			rows, err = ParseVSXList(b)
		}
		if err != nil {
			out.Failed[s.SourceID] = err.Error()
			fails++
			continue
		}
		for _, r := range rows {
			out.Found[s.SourceID] = append(out.Found[s.SourceID], v.mention(ctx, s, r, mine))
		}
	}
	if fails == len(stars) && fails > 0 {
		return out, fmt.Errorf("vsx: every search failed")
	}
	return out, nil
}

func (v *VSX) mention(ctx context.Context, s Star, r VSXEntry, mine string) Mention {
	link := v.base() + "?view=detail.top&oid=" + url.QueryEscape(r.OID)
	m := Mention{Key: "oid:" + r.OID, Title: r.Name, URL: link}
	parts := []string{}
	if r.VariabilityType != "" {
		parts = append(parts, "Listed as "+r.VariabilityType)
	}
	if r.Period != "" {
		parts = append(parts, "period "+r.Period+" d")
	}
	if r.Discoverer != "" {
		parts = append(parts, `discoverer "`+r.Discoverer+`"`)
	}
	ra, err1 := strconv.ParseFloat(r.RA, 64)
	dec, err2 := strconv.ParseFloat(r.Dec, 64)
	if err1 == nil && err2 == nil {
		parts = append(parts, fmt.Sprintf(`%.1f" from your star`, SepArcsec(s.RA, s.Dec, ra, dec)))
	}
	m.Detail = strings.Join(parts, ", ") + "."
	if mine != "" && strings.Contains(strings.ToLower(r.Discoverer), strings.ToLower(mine)) {
		m.Own = true
	}
	if page, err := get(ctx, v.Client, link, 8<<20); err == nil {
		m.Dated = ParseVSXAdded(page)
	}
	return m
}
