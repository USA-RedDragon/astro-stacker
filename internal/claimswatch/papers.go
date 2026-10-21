package claimswatch

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// names are the phrases a paper would name the star by.
func names(s Star) []string {
	var out []string
	if s.GaiaID != 0 {
		out = append(out, fmt.Sprintf("%d", s.GaiaID))
	}
	if j := strings.TrimSpace(strings.TrimPrefix(s.TwoMASS, "2MASS")); j != "" {
		out = append(out, j)
	}
	return out
}

// ArXiv searches arXiv's listing (titles, abstracts and comments) for the
// star's Gaia DR3 id and 2MASS name, waiting Gap between searches as
// arXiv asks.
type ArXiv struct {
	Client *http.Client
	URL    string
	Gap    time.Duration
}

const arxivAPI = "https://export.arxiv.org/api/query"

func (a *ArXiv) Where() string { return WhereArXiv }
func (a *ArXiv) Off() string   { return "" }

type arxivFeed struct {
	Entries []struct {
		ID        string `xml:"id"`
		Title     string `xml:"title"`
		Published string `xml:"published"`
		Authors   []struct {
			Name string `xml:"name"`
		} `xml:"author"`
	} `xml:"entry"`
}

// ParseArXiv reads an arXiv API Atom feed into mentions.
func ParseArXiv(b []byte) ([]Mention, error) {
	var f arxivFeed
	if err := xml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("arxiv: unexpected answer: %w", err)
	}
	var out []Mention
	for _, e := range f.Entries {
		id := e.ID[strings.LastIndex(e.ID, "/")+1:]
		if id == "" {
			continue
		}
		m := Mention{Key: "arxiv:" + id, Title: "arXiv " + id, URL: e.ID, Detail: strings.Join(strings.Fields(e.Title), " ")}
		if len(e.Authors) > 0 {
			m.Detail += ", by " + e.Authors[0].Name
			if len(e.Authors) > 1 {
				m.Detail += " et al."
			}
		}
		if t, err := time.Parse(time.RFC3339, e.Published); err == nil {
			m.Dated = day(t)
		}
		out = append(out, m)
	}
	return out, nil
}

func (a *ArXiv) Check(ctx context.Context, stars []Star, _ State) (Outcome, error) {
	out := newOutcome()
	u := a.URL
	if u == "" {
		u = arxivAPI
	}
	gap := a.Gap
	if gap == 0 {
		gap = 3 * time.Second
	}
	fails := 0
	for i, s := range stars {
		ns := names(s)
		if len(ns) == 0 {
			continue
		}
		if i > 0 && gap > 0 {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(gap):
			}
		}
		terms := make([]string, len(ns))
		for i, n := range ns {
			terms[i] = `all:"` + n + `"`
		}
		q := url.Values{"search_query": {strings.Join(terms, " OR ")}, "max_results": {"50"}}
		b, err := get(ctx, a.Client, u+"?"+q.Encode(), 8<<20)
		var ms []Mention
		if err == nil {
			ms, err = ParseArXiv(b)
		}
		if err != nil {
			out.Failed[s.SourceID] = err.Error()
			fails++
			continue
		}
		out.Found[s.SourceID] = ms
	}
	if fails == len(stars) && fails > 0 {
		return out, fmt.Errorf("arxiv: every search failed")
	}
	return out, nil
}

// ADS searches the full text of the papers in NASA ADS. It needs an ADS
// API token; without one the place is shown as not checked.
type ADS struct {
	Client *http.Client
	URL    string
	Token  func() string
}

const adsAPI = "https://api.adsabs.harvard.edu/v1/search/query"

func (a *ADS) Where() string { return WhereADS }

func (a *ADS) token() string {
	if a.Token == nil {
		return ""
	}
	return strings.TrimSpace(a.Token())
}

func (a *ADS) Off() string {
	if a.token() == "" {
		return "Needs an ADS key, which is not set up yet. Papers SIMBAD links to the star are checked under SIMBAD."
	}
	return ""
}

// ParseADS reads an ADS search answer into mentions; entry_date, when
// ADS added the paper, dates them.
func ParseADS(b []byte) ([]Mention, error) {
	var r struct {
		Response struct {
			Docs []struct {
				Bibcode   string   `json:"bibcode"`
				Title     []string `json:"title"`
				EntryDate string   `json:"entry_date"`
				Author    []string `json:"author"`
			} `json:"docs"`
		} `json:"response"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("ads: unexpected answer: %w", err)
	}
	var out []Mention
	for _, d := range r.Response.Docs {
		m := Mention{Key: "bib:" + d.Bibcode, Title: d.Bibcode, URL: "https://ui.adsabs.harvard.edu/abs/" + url.PathEscape(d.Bibcode)}
		if len(d.Title) > 0 {
			m.Detail = d.Title[0]
		}
		if len(d.Author) > 0 {
			m.Detail += ", by " + d.Author[0]
			if len(d.Author) > 1 {
				m.Detail += " et al."
			}
		}
		if t, err := time.Parse(time.RFC3339, d.EntryDate); err == nil {
			m.Dated = day(t)
		}
		out = append(out, m)
	}
	return out, nil
}

func (a *ADS) Check(ctx context.Context, stars []Star, _ State) (Outcome, error) {
	out := newOutcome()
	tok := a.token()
	if tok == "" {
		return out, fmt.Errorf("ads: no key")
	}
	u := a.URL
	if u == "" {
		u = adsAPI
	}
	fails := 0
	for _, s := range stars {
		ns := names(s)
		if len(ns) == 0 {
			continue
		}
		terms := make([]string, len(ns))
		for i, n := range ns {
			terms[i] = `full:"` + n + `"`
		}
		q := url.Values{"q": {strings.Join(terms, " OR ")}, "fl": {"bibcode,title,entry_date,author"}, "rows": {"100"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
		if err != nil {
			return out, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		b, err := do(a.Client, req, 8<<20)
		var ms []Mention
		if err == nil {
			ms, err = ParseADS(b)
		}
		if err != nil {
			out.Failed[s.SourceID] = err.Error()
			fails++
			continue
		}
		out.Found[s.SourceID] = ms
	}
	if fails == len(stars) && fails > 0 {
		return out, fmt.Errorf("ads: every search failed")
	}
	return out, nil
}
