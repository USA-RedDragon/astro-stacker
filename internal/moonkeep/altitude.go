package moonkeep

import (
	"math"
	"sort"
	"strings"
	"time"
)

// AltTable is the scheduler table the altitude kept rates are published in.
const AltTable = "ts_alt_keep"

// ClassAltStrength is how many subs' worth a class's rate at every
// altitude counts for in one of its altitude bins. It is weak because that
// rate leaves out the very trend the bins measure.
const ClassAltStrength = 2.0

// AltReferenceFrom is where the reference bin starts: a filter's kept rate
// at and above it is what its worth at lower altitudes is measured against.
const AltReferenceFrom = 40.0

// TargetAltEdges are the bins of the target's altitude at mid-exposure, in
// degrees. The last bin is the reference.
func TargetAltEdges() []float64 { return []float64{0, 5, 10, 15, 20, 25, 30, 35, AltReferenceFrom, 90} }

// AltRow is the kept rate of one filter, or of all broadband or all
// narrowband filters when Filter is empty, with the target in one altitude
// bin and the Moon down.
type AltRow struct {
	Filter     string  `json:"filter"`
	Narrowband bool    `json:"narrowband"`
	Bin        int     `json:"alt_bin"`
	From       float64 `json:"alt_from"`
	To         float64 `json:"alt_to"`
	Subs       int     `json:"subs"`
	Kept       int     `json:"kept"`
	Nights     int     `json:"nights"`
	Rate       float64 `json:"rate"`
	Low        float64 `json:"low"`
	High       float64 `json:"high"`
	Worth      float64 `json:"worth"`
	Usable     bool    `json:"usable"`
	Skip       bool    `json:"skip"`
}

// AltModel is a fitted altitude table.
type AltModel struct {
	Rows     []AltRow
	byFilter map[string]map[int]*AltRow
	byClass  map[bool]map[int]*AltRow
}

func targetAltBin(alt float64) int {
	e := TargetAltEdges()
	if math.IsNaN(alt) || alt < e[0] {
		return -1
	}
	for i := 1; i < len(e); i++ {
		if alt < e[i] {
			return i - 1
		}
	}
	return len(e) - 2
}

type altKey struct {
	cl  class
	bin int
}

// FitAltitude fits the altitude rows from subs taken with the Moon down,
// so the Moon's own effect is left to the Moon rows. Each bin of a class
// is shrunk a little toward the class's rate at every altitude, and each
// filter's bin toward its class's bin with PriorStrength subs.
func FitAltitude(samples []Sample) *AltModel { return fitAltitude(samples, false) }

func fitAltitude(samples []Sample, quick bool) *AltModel {
	agg := map[altKey]*counts{}
	all := map[bool]*counts{}
	filters := map[class]bool{}
	for _, s := range samples {
		if s.Moon.Up() {
			continue
		}
		b := targetAltBin(s.Altitude)
		if b < 0 {
			continue
		}
		if all[s.Narrowband] == nil {
			all[s.Narrowband] = &counts{}
		}
		all[s.Narrowband].add(s)
		keys := []altKey{{class{nb: s.Narrowband}, b}}
		if s.Filter != "" {
			fl := class{nb: s.Narrowband, filter: s.Filter}
			filters[fl] = true
			keys = append(keys, altKey{fl, b})
		}
		for _, k := range keys {
			if agg[k] == nil {
				agg[k] = &counts{}
			}
			agg[k].add(s)
		}
	}
	fls := make([]class, 0, len(filters))
	for f := range filters {
		fls = append(fls, f)
	}
	sort.Slice(fls, func(i, j int) bool { return fls[i].filter < fls[j].filter })
	m := &AltModel{}
	ref := len(TargetAltEdges()) - 2
	for _, nb := range []bool{false, true} {
		cl := class{nb: nb}
		base := shrink(root(), 2, all[nb])
		classEst := make([]estimate, ref+1)
		for b := 0; b <= ref; b++ {
			classEst[b] = shrink(base, ClassAltStrength, agg[altKey{cl, b}])
		}
		for b := 0; b <= ref; b++ {
			m.add(cl, b, classEst[b], classEst[ref].mean(), quick)
		}
		for _, f := range fls {
			if f.nb != nb {
				continue
			}
			own := make([]estimate, ref+1)
			for b := 0; b <= ref; b++ {
				own[b] = shrink(classEst[b], PriorStrength, agg[altKey{f, b}])
			}
			for b := 0; b <= ref; b++ {
				if agg[altKey{f, b}] == nil {
					continue
				}
				m.add(f, b, own[b], own[ref].mean(), quick)
			}
		}
	}
	m.index()
	return m
}

func (m *AltModel) add(cl class, b int, e estimate, refRate float64, quick bool) {
	edges := TargetAltEdges()
	r := AltRow{Filter: cl.filter, Narrowband: cl.nb, Bin: b, From: edges[b], To: edges[b+1]}
	if e.c != nil {
		r.Subs, r.Kept, r.Nights = e.c.subs, e.c.kept, len(e.c.nights)
	}
	r.Rate = round(e.mean())
	ba, bb := e.bounds()
	if !quick {
		r.Low = round(BetaQuantile(1-Confidence, ba, bb))
		r.High = round(BetaQuantile(Confidence, ba, bb))
	}
	if refRate > 0 {
		r.Worth = round(math.Min(1, e.mean()/refRate))
	}
	r.Usable = r.Subs >= MinSubs && r.Nights >= MinNights
	r.Skip = r.Usable && BetaCDF(SkipBelow, ba, bb) > Confidence
	m.Rows = append(m.Rows, r)
}

func (m *AltModel) index() {
	m.byFilter = map[string]map[int]*AltRow{}
	m.byClass = map[bool]map[int]*AltRow{}
	for i := range m.Rows {
		r := &m.Rows[i]
		if r.Filter == "" {
			if m.byClass[r.Narrowband] == nil {
				m.byClass[r.Narrowband] = map[int]*AltRow{}
			}
			m.byClass[r.Narrowband][r.Bin] = r
			continue
		}
		f := normFilter(r.Filter)
		if m.byFilter[f] == nil {
			m.byFilter[f] = map[int]*AltRow{}
		}
		m.byFilter[f][r.Bin] = r
	}
}

// Lookup is the row the plugin uses for a filter at a target altitude: the
// filter's own row when it has enough subs, else its class row when that
// has, else nil (no altitude weight).
func (m *AltModel) Lookup(filter string, narrowband bool, alt float64) *AltRow {
	if m == nil {
		return nil
	}
	b := targetAltBin(alt)
	if b < 0 {
		return nil
	}
	if r := m.byFilter[normFilter(filter)][b]; r != nil && r.Usable {
		return r
	}
	if r := m.byClass[narrowband][b]; r != nil && r.Usable {
		return r
	}
	return nil
}

// AltReplay is the altitude rows run over past nights: each night's
// Moon-down subs judged by rows fitted on every other night.
type AltReplay struct {
	Nights        int     `json:"nights"`
	Subs          int     `json:"subs"`
	Kept          int     `json:"kept"`
	Hours         float64 `json:"hours"`
	SkippedSubs   int     `json:"skipped_subs"`
	SkippedHours  float64 `json:"skipped_hours"`
	KeptLost      int     `json:"kept_lost"`
	Weighted      int     `json:"weighted_subs"`
	WorthKept     float64 `json:"mean_worth_kept"`
	WorthLeft     float64 `json:"mean_worth_not_kept"`
	SubsUnderHalf int     `json:"subs_under_half_worth"`
	KeptUnderHalf int     `json:"kept_under_half_worth"`
}

// RunAltitude replays the altitude rows over every night with Moon-down
// subs, each judged by rows fitted without it.
func RunAltitude(samples []Sample) AltReplay {
	var out AltReplay
	byNight := map[string][]int{}
	for i, s := range samples {
		if s.Moon.Up() || targetAltBin(s.Altitude) < 0 {
			continue
		}
		byNight[s.Night] = append(byNight[s.Night], i)
	}
	names := make([]string, 0, len(byNight))
	for n := range byNight {
		names = append(names, n)
	}
	sort.Strings(names)
	out.Nights = len(names)
	var sumKept, sumLeft float64
	var nKept, nLeft int
	for _, n := range names {
		rest := make([]Sample, 0, len(samples))
		for _, s := range samples {
			if s.Night != n {
				rest = append(rest, s)
			}
		}
		m := fitAltitude(rest, false)
		for _, i := range byNight[n] {
			s := samples[i]
			h := s.Exposure / 3600
			out.Subs++
			out.Hours += h
			if s.Kept {
				out.Kept++
			}
			r := m.Lookup(s.Filter, s.Narrowband, s.Altitude)
			if r == nil {
				continue
			}
			out.Weighted++
			if s.Kept {
				sumKept += r.Worth
				nKept++
			} else {
				sumLeft += r.Worth
				nLeft++
			}
			if r.Worth < 0.5 {
				out.SubsUnderHalf++
				if s.Kept {
					out.KeptUnderHalf++
				}
			}
			if r.Skip {
				out.SkippedSubs++
				out.SkippedHours += h
				if s.Kept {
					out.KeptLost++
				}
			}
		}
	}
	if nKept > 0 {
		out.WorthKept = round(sumKept / float64(nKept))
	}
	if nLeft > 0 {
		out.WorthLeft = round(sumLeft / float64(nLeft))
	}
	out.Hours = math.Round(out.Hours*10) / 10
	out.SkippedHours = math.Round(out.SkippedHours*10) / 10
	return out
}

// AltTableRow is one row of ts_alt_keep.
type AltTableRow struct {
	Filter     string    `gorm:"column:filter"`
	Narrowband int       `gorm:"column:narrowband"`
	AltBin     int       `gorm:"column:alt_bin"`
	AltFrom    float64   `gorm:"column:alt_from"`
	AltTo      float64   `gorm:"column:alt_to"`
	Subs       int       `gorm:"column:subs"`
	Kept       int       `gorm:"column:kept"`
	Nights     int       `gorm:"column:nights"`
	Rate       float64   `gorm:"column:rate"`
	RateLow    float64   `gorm:"column:rate_low"`
	RateHigh   float64   `gorm:"column:rate_high"`
	Worth      float64   `gorm:"column:worth"`
	Usable     int       `gorm:"column:usable"`
	Skips      int       `gorm:"column:skips"`
	ComputedAt time.Time `gorm:"column:computed_at"`
}

type altRowKey struct {
	filter     string
	narrowband int
	bin        int
}

func (r AltTableRow) key() altRowKey { return altRowKey{r.Filter, r.Narrowband, r.AltBin} }

func (r AltTableRow) same(o AltTableRow) bool {
	return near(r.AltFrom, o.AltFrom) && near(r.AltTo, o.AltTo) && r.Subs == o.Subs && r.Kept == o.Kept && r.Nights == o.Nights &&
		near(r.Rate, o.Rate) && near(r.RateLow, o.RateLow) && near(r.RateHigh, o.RateHigh) && near(r.Worth, o.Worth) && r.Usable == o.Usable && r.Skips == o.Skips
}

// AltTableRows is what ts_alt_keep gets from a fit: every class row and the
// filter rows with subs.
func AltTableRows(m *AltModel, at time.Time) []AltTableRow {
	var out []AltTableRow
	if m == nil {
		return out
	}
	for _, r := range m.Rows {
		if r.Filter != "" && r.Subs == 0 {
			continue
		}
		out = append(out, AltTableRow{Filter: strings.TrimSpace(r.Filter), Narrowband: b2i(r.Narrowband), AltBin: r.Bin, AltFrom: r.From, AltTo: r.To,
			Subs: r.Subs, Kept: r.Kept, Nights: r.Nights, Rate: r.Rate, RateLow: r.Low, RateHigh: r.High, Worth: r.Worth,
			Usable: b2i(r.Usable), Skips: b2i(r.Skip), ComputedAt: at.UTC()})
	}
	return out
}

const AltSchema = `create table if not exists ts_alt_keep (
    filter text NOT NULL,
    narrowband integer NOT NULL,
    alt_bin integer NOT NULL,
    alt_from double precision NOT NULL,
    alt_to double precision NOT NULL,
    subs integer NOT NULL,
    kept integer NOT NULL,
    nights integer NOT NULL,
    rate double precision NOT NULL,
    rate_low double precision NOT NULL,
    rate_high double precision NOT NULL,
    worth double precision NOT NULL,
    usable integer NOT NULL,
    skips integer NOT NULL,
    computed_at timestamp NOT NULL,
    PRIMARY KEY (filter, narrowband, alt_bin)
)`
