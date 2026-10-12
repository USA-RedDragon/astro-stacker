// Package moonkeep learns how many subs the stacker keeps under each Moon:
// its kept rate by filter, Moon phase, Moon altitude and distance from the
// target, fitted from the stacker's own verdicts and published for the
// scheduler plugin in ts_moon_keep.
package moonkeep

import (
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
)

const (
	lunarCycleDays = 29.53059
	// FloorDegrees is the moon floor: the plugin never images with the
	// Moon up and nearer than this, so those subs are left out of the fit.
	FloorDegrees = 20.0
	// PriorStrength is how many subs' worth the coarser estimate counts
	// for in a finer one. 40 had the lowest leave-one-night-out log loss
	// of 5, 10, 20, 40, 60 and 80 on 19,555 subs (2026-10-12).
	PriorStrength = 40.0
	// MinSubs and MinNights are what a row needs before the plugin acts on
	// it; with fewer it keeps today's moon glare rule.
	MinSubs   = 30
	MinNights = 3
	// SkipBelow is the kept rate a row's upper bound must be under for the
	// plugin to leave the time to darks, flats or other filters.
	SkipBelow = 0.05
	// GoodNightQuality is the measured night quality at or above which no
	// row skips: past bright-Moon hours after such subs kept 25 to 75%.
	GoodNightQuality = 0.3
	// Confidence is the one-sided level of the upper bound.
	Confidence = 0.95
)

// IllumEdges, AltEdges and SepEdges are the bin edges of the Moon's lit
// fraction, altitude (degrees) and distance from the target (degrees).
func IllumEdges() []float64 { return []float64{0, 0.25, 0.5, 0.75, 0.9, 1} }
func AltEdges() []float64   { return []float64{0, 15, 35, 90} }
func SepEdges() []float64   { return []float64{FloorDegrees, 45, 75, 110, 180} }

// State is the Moon as the plugin sees it for one exposure.
type State struct {
	Illumination float64 `json:"illumination"`
	Altitude     float64 `json:"altitude"`
	Separation   float64 `json:"separation"`
}

func (s State) Up() bool { return s.Altitude > 0 }

// Illumination is the lit fraction the plugin uses, from the Moon's age.
func Illumination(age float64) float64 {
	return (1 - math.Cos(2*math.Pi*age/lunarCycleDays)) / 2
}

// At is the Moon seen from lat, lon at t, and its distance from ra, dec
// (degrees).
func At(t time.Time, ra, dec, lat, lon float64) State {
	g := moon.At(t)
	m := g.Topocentric(t, lat, lon)
	return State{Illumination: Illumination(g.Age), Altitude: m.Altitude(t, lat, lon), Separation: m.Separation(ra, dec)}
}

func bin(v float64, edges []float64) int {
	if math.IsNaN(v) || v < edges[0] {
		return -1
	}
	for i := 1; i < len(edges); i++ {
		if v < edges[i] {
			return i - 1
		}
	}
	if v <= edges[len(edges)-1] {
		return len(edges) - 2
	}
	return -1
}

// Cell is one Moon bin: Down for the Moon below the horizon, else the
// phase, altitude and separation bin indexes.
type Cell struct {
	Down        bool
	Illum, Alt  int
	Sep         int
	floorOrMiss bool
}

func CellOf(s State) Cell {
	if !s.Up() {
		return Cell{Down: true}
	}
	c := Cell{Illum: bin(s.Illumination, IllumEdges()), Alt: bin(math.Min(s.Altitude, 90), AltEdges()), Sep: bin(s.Separation, SepEdges())}
	c.floorOrMiss = c.Illum < 0 || c.Alt < 0 || c.Sep < 0
	return c
}

// Valid is false inside the moon floor or when a value is out of range.
func (c Cell) Valid() bool { return !c.floorOrMiss }

// Sample is one sub with the stacker's verdict.
type Sample struct {
	Night      string
	Object     string
	Filter     string
	Narrowband bool
	Kept       bool
	Exposure   float64
	Start      time.Time
	Moon       State
	Quality    *float64
	Altitude   float64
}

// Row is the kept rate of one filter, or of all broadband or all
// narrowband filters when Filter is empty, under one Moon bin.
type Row struct {
	Filter     string  `json:"filter"`
	Narrowband bool    `json:"narrowband"`
	Down       bool    `json:"moon_down"`
	IllumBin   int     `json:"illum_bin"`
	AltBin     int     `json:"alt_bin"`
	SepBin     int     `json:"sep_bin"`
	IllumFrom  float64 `json:"illum_from"`
	IllumTo    float64 `json:"illum_to"`
	AltFrom    float64 `json:"alt_from"`
	AltTo      float64 `json:"alt_to"`
	SepFrom    float64 `json:"sep_from"`
	SepTo      float64 `json:"sep_to"`
	Subs       int     `json:"subs"`
	Kept       int     `json:"kept"`
	Nights     int     `json:"nights"`
	Rate       float64 `json:"rate"`
	Low        float64 `json:"low"`
	High       float64 `json:"high"`
	Worth      float64 `json:"worth"`
	Usable     bool    `json:"usable"`
	Skip       bool    `json:"skip"`
	cell       Cell
}

type counts struct {
	kept, subs int
	nights     map[string][2]int
}

func (c *counts) add(s Sample) {
	if c.nights == nil {
		c.nights = map[string][2]int{}
	}
	n := c.nights[s.Night]
	n[1]++
	c.subs++
	if s.Kept {
		n[0]++
		c.kept++
	}
	c.nights[s.Night] = n
}

// deff is the design effect of subs clustered by night: the cluster-robust
// variance of the kept rate over the binomial one, at least 1. It is 0 when
// the subs can't tell it (all kept or none), and the subs' count for one
// night, which then counts as one sub. A row with none or all kept borrows
// its coarser row's design effect, capped at its subs per night.
func (c *counts) deff() float64 {
	if c == nil || c.subs < 2 {
		return 0
	}
	p := float64(c.kept) / float64(c.subs)
	j := len(c.nights)
	if j < 2 {
		return float64(c.subs)
	}
	if p <= 0 || p >= 1 {
		return 0
	}
	var v float64
	for _, n := range c.nights {
		d := float64(n[0]) - p*float64(n[1])
		v += d * d
	}
	n := float64(c.subs)
	v = v / (n * n) * float64(j) / float64(j-1)
	return math.Max(1, v/(p*(1-p)/n))
}

type estimate struct {
	a, b, deff float64
	kEff, nEff float64
	c          *counts
}

func (e estimate) mean() float64 { return e.a / (e.a + e.b) }

// bounds is the Beta the bounds come from: the row's own subs alone, as
// effective counts with Jeffreys' prior, so the coarser rows (which hold
// the same subs) never narrow them. A row with no subs takes the shrunk
// estimate.
func (e estimate) bounds() (float64, float64) {
	if e.nEff <= 0 {
		return e.a, e.b
	}
	return e.kEff + 0.5, e.nEff - e.kEff + 0.5
}

func shrink(parent estimate, strength float64, c *counts) estimate {
	p := parent.mean()
	e := estimate{a: strength * p, b: strength * (1 - p), deff: parent.deff, c: c}
	if c == nil || c.subs == 0 {
		return e
	}
	if d := c.deff(); d > 0 {
		e.deff = d
	}
	d := math.Max(1, math.Min(e.deff, float64(c.subs)/float64(len(c.nights))))
	e.kEff, e.nEff = float64(c.kept)/d, float64(c.subs)/d
	e.a += e.kEff
	e.b += e.nEff - e.kEff
	return e
}

func root() estimate { return estimate{a: 1, b: 1, deff: 1} }

type class struct {
	nb     bool
	filter string
}

type key struct {
	class
	level int
	cell  Cell
}

func keysOf(s Sample, c Cell) []key {
	cl := class{nb: s.Narrowband}
	fl := class{nb: s.Narrowband, filter: s.Filter}
	var out []key
	if c.Down {
		out = []key{{cl, 0, Cell{Down: true}}, {fl, 0, Cell{Down: true}}}
	} else {
		out = []key{
			{cl, 1, Cell{}},
			{cl, 2, Cell{Illum: c.Illum}},
			{cl, 3, Cell{Illum: c.Illum, Alt: c.Alt}},
			{cl, 4, c},
			{fl, 4, c},
		}
	}
	if s.Filter == "" {
		return out[:len(out)-1]
	}
	return out
}

// Model is a fitted kept-rate table.
type Model struct {
	Rows      []Row
	byFilter  map[string]map[Cell]*Row
	byClass   map[bool]map[Cell]*Row
	Subs      int
	Kept      int
	Nights    int
	From, To  *time.Time
	SkipBelow float64
	quick     bool
}

// Fit fits the table from samples; a sample with an invalid cell (inside
// the moon floor) is left out.
func Fit(samples []Sample) *Model { return fit(samples, false, SkipBelow) }

func fit(samples []Sample, quick bool, skipBelow float64) *Model {
	agg := map[key]*counts{}
	filters := map[class]bool{}
	nights := map[string]bool{}
	m := &Model{SkipBelow: skipBelow, quick: quick}
	for _, s := range samples {
		c := CellOf(s.Moon)
		if !c.Down && !c.Valid() {
			continue
		}
		m.Subs++
		if s.Kept {
			m.Kept++
		}
		nights[s.Night] = true
		if m.From == nil || s.Start.Before(*m.From) {
			t := s.Start
			m.From = &t
		}
		if m.To == nil || s.Start.After(*m.To) {
			t := s.Start
			m.To = &t
		}
		if s.Filter != "" {
			filters[class{nb: s.Narrowband, filter: s.Filter}] = true
		}
		for _, k := range keysOf(s, c) {
			if agg[k] == nil {
				agg[k] = &counts{}
			}
			agg[k].add(s)
		}
	}
	m.Nights = len(nights)
	est := func(k key, parent estimate) estimate {
		if k.level == 0 && k.filter == "" || k.level == 1 {
			return shrink(root(), 2, agg[k])
		}
		return shrink(parent, PriorStrength, agg[k])
	}
	down := map[bool]estimate{}
	downRate := map[class]float64{}
	for _, nb := range []bool{false, true} {
		cl := class{nb: nb}
		down[nb] = est(key{cl, 0, Cell{Down: true}}, root())
		downRate[cl] = down[nb].mean()
	}
	fls := make([]class, 0, len(filters))
	for f := range filters {
		fls = append(fls, f)
		if c := agg[key{f, 0, Cell{Down: true}}]; c != nil {
			e := est(key{f, 0, Cell{Down: true}}, down[f.nb])
			downRate[f] = e.mean()
			m.add(f, Cell{Down: true}, e, downRate[f])
		}
	}
	sort.Slice(fls, func(i, j int) bool { return fls[i].filter < fls[j].filter })
	for _, nb := range []bool{false, true} {
		cl := class{nb: nb}
		m.add(cl, Cell{Down: true}, down[nb], downRate[cl])
		up := est(key{cl, 1, Cell{}}, root())
		for i := 0; i < len(IllumEdges())-1; i++ {
			ill := est(key{cl, 2, Cell{Illum: i}}, up)
			for a := 0; a < len(AltEdges())-1; a++ {
				alt := est(key{cl, 3, Cell{Illum: i, Alt: a}}, ill)
				for p := 0; p < len(SepEdges())-1; p++ {
					c := Cell{Illum: i, Alt: a, Sep: p}
					ce := est(key{cl, 4, c}, alt)
					m.add(cl, c, ce, downRate[cl])
					for _, f := range fls {
						if f.nb != nb || agg[key{f, 4, c}] == nil {
							continue
						}
						dr, ok := downRate[f]
						if !ok {
							dr = downRate[cl]
						}
						m.add(f, c, est(key{f, 4, c}, ce), dr)
					}
				}
			}
		}
	}
	m.index()
	return m
}

func (m *Model) add(cl class, c Cell, e estimate, downRate float64) {
	r := Row{Filter: cl.filter, Narrowband: cl.nb, Down: c.Down, cell: c, IllumBin: c.Illum, AltBin: c.Alt, SepBin: c.Sep}
	if c.Down {
		r.IllumBin, r.AltBin, r.SepBin = -1, -1, -1
		r.IllumFrom, r.IllumTo, r.AltFrom, r.AltTo, r.SepFrom, r.SepTo = 0, 1, -90, 0, 0, 180
	} else {
		ie, ae, se := IllumEdges(), AltEdges(), SepEdges()
		r.IllumFrom, r.IllumTo = ie[c.Illum], ie[c.Illum+1]
		r.AltFrom, r.AltTo = ae[c.Alt], ae[c.Alt+1]
		r.SepFrom, r.SepTo = se[c.Sep], se[c.Sep+1]
	}
	if e.c != nil {
		r.Subs, r.Kept, r.Nights = e.c.subs, e.c.kept, len(e.c.nights)
	}
	r.Rate = round(e.mean())
	ba, bb := e.bounds()
	if !m.quick {
		r.Low = round(BetaQuantile(1-Confidence, ba, bb))
		r.High = round(BetaQuantile(Confidence, ba, bb))
	}
	if downRate > 0 {
		r.Worth = round(math.Min(1, e.mean()/downRate))
	}
	r.Usable = r.Subs >= MinSubs && r.Nights >= MinNights
	r.Skip = r.Usable && !r.Down && BetaCDF(m.SkipBelow, ba, bb) > Confidence
	m.Rows = append(m.Rows, r)
}

func round(v float64) float64 { return math.Round(v*1e4) / 1e4 }

func (m *Model) index() {
	m.byFilter = map[string]map[Cell]*Row{}
	m.byClass = map[bool]map[Cell]*Row{}
	for i := range m.Rows {
		r := &m.Rows[i]
		if r.Filter == "" {
			if m.byClass[r.Narrowband] == nil {
				m.byClass[r.Narrowband] = map[Cell]*Row{}
			}
			m.byClass[r.Narrowband][r.cell] = r
			continue
		}
		f := normFilter(r.Filter)
		if m.byFilter[f] == nil {
			m.byFilter[f] = map[Cell]*Row{}
		}
		m.byFilter[f][r.cell] = r
	}
}

func normFilter(f string) string { return strings.ToUpper(strings.TrimSpace(f)) }

// Lookup is the row the plugin uses for a filter under a Moon: the filter's
// own row when it has enough subs, else its class row when that has, else
// nil (the plugin keeps the moon glare rule).
func (m *Model) Lookup(filter string, narrowband bool, s State) *Row {
	if m == nil {
		return nil
	}
	c := CellOf(s)
	if !c.Down && !c.Valid() {
		return nil
	}
	if r := m.byFilter[normFilter(filter)][c]; r != nil && r.Usable {
		return r
	}
	if r := m.byClass[narrowband][c]; r != nil && r.Usable {
		return r
	}
	return nil
}

// Skips reports whether the plugin leaves an exposure of the filter under
// the Moon s to other filters, darks or flats, given the night quality.
func (m *Model) Skips(filter string, narrowband bool, s State, quality *float64) bool {
	r := m.Lookup(filter, narrowband, s)
	return r != nil && r.Skip && (quality == nil || *quality < GoodNightQuality)
}

// ClassRows is the class rows (all broadband or all narrowband filters).
func (m *Model) ClassRows(narrowband bool) []Row {
	var out []Row
	for _, r := range m.Rows {
		if r.Filter == "" && r.Narrowband == narrowband {
			out = append(out, r)
		}
	}
	return out
}

// Filters is the filters with rows of their own.
func (m *Model) Filters() []string {
	var out []string
	for _, r := range m.Rows {
		if r.Filter != "" && !slices.Contains(out, r.Filter) {
			out = append(out, r.Filter)
		}
	}
	slices.Sort(out)
	return out
}
