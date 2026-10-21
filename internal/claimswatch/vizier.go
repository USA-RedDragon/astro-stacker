package claimswatch

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// VizieR keeps the list of VizieR's variable star tables and searches
// every table that appears after the first check within Radius
// arcseconds of each star. The tables there at the first check are not
// searched one by one; VSX and SIMBAD cover the older catalogs.
type VizieR struct {
	Client *http.Client
	TAPURL string
	ASUURL string
	Radius float64
}

const (
	vizierTAP       = "https://tapvizier.cds.unistra.fr/TAPVizieR/tap/sync"
	vizierASU       = "https://vizier.cds.unistra.fr/viz-bin/asu-tsv"
	stateVizieRList = "vizier:tables"
)

func (v *VizieR) Where() string { return WhereVizieR }
func (v *VizieR) Off() string   { return "" }

// VizieRTable is a table's name and description.
type VizieRTable struct{ Name, Title string }

// ParseVizieRTables reads the TAP answer listing the tables.
func ParseVizieRTables(b []byte) ([]VizieRTable, error) {
	if !bytes.HasPrefix(b, []byte("table_name\t")) {
		return nil, fmt.Errorf("vizier: unexpected answer: %.160s", b)
	}
	var out []VizieRTable
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	sc.Scan()
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 2 {
			continue
		}
		name := strings.Trim(strings.Trim(f[0], `"`), "'")
		out = append(out, VizieRTable{Name: name, Title: strings.Trim(f[1], `"`)})
	}
	return out, sc.Err()
}

// ParseVizieRCone counts the rows of an asu-tsv cone search and gives the
// nearest distance in arcseconds.
func ParseVizieRCone(b []byte) (int, float64) {
	n, best := 0, -1.0
	data := false
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "--"):
			data = true
			continue
		}
		if !data {
			continue
		}
		f := strings.SplitN(line, "\t", 2)
		r, err := strconv.ParseFloat(strings.TrimSpace(f[0]), 64)
		if err != nil {
			continue
		}
		n++
		if best < 0 || r < best {
			best = r
		}
	}
	return n, best
}

func (v *VizieR) tables(ctx context.Context) ([]VizieRTable, error) {
	u := v.TAPURL
	if u == "" {
		u = vizierTAP
	}
	form := url.Values{"REQUEST": {"doQuery"}, "LANG": {"ADQL"}, "FORMAT": {"tsv"}, "QUERY": {
		"SELECT table_name, description FROM TAP_SCHEMA.tables WHERE description LIKE '%ariab%' OR description LIKE '%clipsing%'"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	b, err := do(v.Client, req, 32<<20)
	if err != nil {
		return nil, err
	}
	return ParseVizieRTables(b)
}

func (v *VizieR) Check(ctx context.Context, stars []Star, st State) (Outcome, error) {
	out := newOutcome()
	tabs, err := v.tables(ctx)
	if err != nil {
		return out, fmt.Errorf("vizier: %w", err)
	}
	if len(tabs) == 0 {
		return out, fmt.Errorf("vizier: no variable star tables listed")
	}
	prev, _ := st.Get(ctx, stateVizieRList)
	known := map[string]bool{}
	for n := range strings.SplitSeq(prev, "\n") {
		if n != "" {
			known[n] = true
		}
	}
	var fresh []VizieRTable
	if prev != "" {
		for _, t := range tabs {
			if !known[t.Name] {
				fresh = append(fresh, t)
			}
		}
	}
	radius := v.Radius
	if radius <= 0 {
		radius = 5
	}
	asu := v.ASUURL
	if asu == "" {
		asu = vizierASU
	}
	for _, t := range fresh {
		for _, s := range stars {
			q := url.Values{"-source": {t.Name}, "-c": {fmt.Sprintf("%.6f %+.6f", s.RA, s.Dec)}, "-c.rs": {strconv.FormatFloat(radius, 'f', 1, 64)},
				"-out": {"_r"}, "-out.max": {"20"}}
			b, err := get(ctx, v.Client, asu+"?"+q.Encode(), 4<<20)
			if err != nil {
				out.Failed[s.SourceID] = err.Error()
				continue
			}
			if n, sep := ParseVizieRCone(b); n > 0 {
				out.Found[s.SourceID] = append(out.Found[s.SourceID], Mention{Key: "table:" + t.Name, Title: t.Name,
					Detail: fmt.Sprintf(`New variable star table "%s" lists a star %.1f" from yours.`, t.Title, sep),
					URL:    "https://vizier.cds.unistra.fr/viz-bin/VizieR-3?-source=" + url.QueryEscape(t.Name)})
			}
		}
	}
	names := make([]string, 0, len(tabs)+len(known))
	for _, t := range tabs {
		names = append(names, t.Name)
		delete(known, t.Name)
	}
	for n := range known {
		names = append(names, n)
	}
	sort.Strings(names)
	return out, st.Set(ctx, stateVizieRList, strings.Join(names, "\n"))
}
