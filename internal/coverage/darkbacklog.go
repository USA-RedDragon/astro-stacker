package coverage

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/darkcheck"
	"gorm.io/gorm"
)

type DarkBacklog struct {
	ComputedAt        time.Time         `json:"computed_at"`
	FramesNeeded      int               `json:"frames_needed"`
	FramesNeededBasis string            `json:"frames_needed_basis"`
	SetTempExactC     float64           `json:"set_temp_exact_c"`
	SessionGapHours   float64           `json:"session_gap_hours"`
	Combos            []DarkCombo       `json:"combos"`
	Unschedulable     []DarkUnscheduled `json:"unschedulable"`
	LastSession       *DarkSession      `json:"last_session"`
	Rejected          []RejectedDark    `json:"rejected"`
	Unchecked         int               `json:"unchecked"`
}

type DarkCombo struct {
	ComboKey        string     `json:"combo_key"`
	Priority        int        `json:"priority"`
	Exposure        float64    `json:"exposure"`
	Gain            float64    `json:"gain"`
	Offset          float64    `json:"offset"`
	Binning         float64    `json:"binning"`
	ReadoutMode     *string    `json:"readout_mode"`
	SetTemp         float64    `json:"set_temp"`
	SetTempsCovered []float64  `json:"set_temps_covered"`
	FramesNeeded    int        `json:"frames_needed"`
	FramesHave      int        `json:"frames_have"`
	FramesRejected  int        `json:"frames_rejected"`
	LightsBlocked   int        `json:"lights_blocked"`
	LightsScaled    int        `json:"lights_scaled"`
	LightsThin      int        `json:"lights_thin"`
	Nights          int        `json:"nights"`
	LatestNight     string     `json:"latest_night"`
	NewestDarkAt    *time.Time `json:"newest_dark_at"`
	Basis           string     `json:"basis"`
}

type DarkUnscheduled struct {
	Exposure *float64 `json:"exposure"`
	SetTemp  *float64 `json:"set_temp"`
	Lights   int      `json:"lights"`
	Reason   string   `json:"reason"`
}

type DarkSession struct {
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Frames    int       `json:"frames"`
	Clean     int       `json:"clean"`
	Leak      int       `json:"leak"`
	Unchecked int       `json:"unchecked"`
}

type RejectedDark struct {
	Key       string     `json:"key"`
	TakenAt   *time.Time `json:"taken_at"`
	Exposure  *float64   `json:"exposure"`
	Gain      *float64   `json:"gain"`
	Offset    *float64   `json:"offset"`
	SetTemp   *float64   `json:"set_temp"`
	SpreadADU *float64   `json:"spread_adu"`
	MedianADU *float64   `json:"median_adu"`
	Reason    string     `json:"reason"`
}

const RejectedListed = 50

const DarkFramesTarget = 25

type cleanSets struct {
	median, sets int
}

func FramesNeededBasis(c cleanSets) string {
	measured := "No dark set on record has a clean dark yet."
	if c.sets > 0 {
		measured = fmt.Sprintf("The %d dark sets on record have a median of %d clean darks each (ingest check verdict clean, not rejected).", c.sets, c.median)
	}
	return fmt.Sprintf("The target of %d darks in one set is the user's choice, to match the dark sets already taken. %s "+
		"The master-dark builder still accepts a set of %d or more (calmatch.MinFrames), so a combo whose exact dark set has %d to %d clean darks "+
		"is calibrated with it and stays on this backlog until a set of %d is taken.",
		DarkFramesTarget, measured, calmatch.MinFrames, calmatch.MinFrames, DarkFramesTarget-1, DarkFramesTarget)
}

func clean(f darkFrame) bool {
	return f.LightLeak == nil && f.CalCheck != nil && *f.CalCheck == darkcheck.StateClean
}

func rejected(f darkFrame) bool {
	return f.LightLeak != nil || (f.CalCheck != nil && (*f.CalCheck == darkcheck.StateLeak || *f.CalCheck == darkcheck.StateOffTemp))
}

func cleanInSet(s calmatch.Set, darks []darkFrame) int {
	n := 0
	for _, f := range darks {
		if !clean(f) || f.Exposure == nil || f.Gain == nil || f.Offset == nil || f.SetTemp == nil {
			continue
		}
		if *f.Exposure != s.Exposure || *f.Gain != s.Gain || *f.Offset != s.Offset || *f.SetTemp != s.SetTemp || !same(val(f.BinX), s.BinX) {
			continue
		}
		if at := f.at(); !at.Before(s.From) && !at.After(s.To) {
			n++
		}
	}
	return n
}

func measureCleanSets(sets []calmatch.Set, darks []darkFrame) (map[int]int, cleanSets) {
	counts := map[int]int{}
	var all []int
	for i, s := range sets {
		if s.Type != darkType || s.Master != "" {
			continue
		}
		counts[i] = cleanInSet(s, darks)
		if counts[i] > 0 {
			all = append(all, counts[i])
		}
	}
	if len(all) == 0 {
		return counts, cleanSets{}
	}
	sort.Ints(all)
	return counts, cleanSets{median: all[len(all)/2], sets: len(all)}
}

func fullExactDark(g calmatch.Group, sets []calmatch.Set, counts map[int]int) bool {
	for i, s := range sets {
		if s.Type != darkType || !calmatch.Buildable(s) || !same(s.Gain, g.Gain) || !same(s.Offset, g.Offset) || !same(s.BinX, g.BinX) {
			continue
		}
		if calmatch.TempOff(g, s) > calmatch.SetTempExactC || math.Abs(s.Exposure-g.Exposure) > calmatch.ExposureTolerance*g.Exposure {
			continue
		}
		if s.Master != "" || counts[i] >= DarkFramesTarget {
			return true
		}
	}
	return false
}

type darkLightGroup struct {
	Night       time.Time
	Exposure    *float64
	Gain        *float64
	Offset      *float64
	SetTemp     *float64
	BinX        *float64
	ReadoutMode *string
	N           int
}

type darkFrame struct {
	Key            string
	Exposure       *float64
	Gain           *float64
	Offset         *float64
	SetTemp        *float64
	BinX           *float64
	DateObs        *time.Time
	Night          *time.Time
	LightLeak      *float64
	CalCheck       *string
	CalCheckReason *string
	CalMedianADU   *float64
	DarkSpread     *float64
}

func (f darkFrame) at() time.Time {
	if f.DateObs != nil {
		return *f.DateObs
	}
	if f.Night != nil {
		return *f.Night
	}
	return time.Time{}
}

type darkSetup struct{ exposure, gain, offset, bin float64 }

func numText(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func ComboKey(exposure, gain, offset, bin, setTemp float64) string {
	return strings.Join([]string{numText(exposure), numText(gain), numText(offset), numText(bin), numText(setTemp)}, "|")
}

type tempTally struct {
	blocked, scaled, thin int
	nights                map[string]bool
	latest                string
	readout               map[string]int
}

func BuildDarkBacklog(ctx context.Context, db *gorm.DB, now time.Time) (DarkBacklog, error) {
	sets, _, err := loadSets(ctx, db)
	if err != nil {
		return DarkBacklog{}, err
	}
	var lights []darkLightGroup
	if err := db.WithContext(ctx).Table("frames").
		Select(`night, exposure, gain, "offset", set_temp, bin_x, readout_mode, COUNT(*) as n`).
		Where("type = ? AND night IS NOT NULL AND index_error IS NULL", "LIGHT").
		Group(`night, exposure, gain, "offset", set_temp, bin_x, readout_mode`).
		Scan(&lights).Error; err != nil {
		return DarkBacklog{}, fmt.Errorf("load lights for the dark backlog: %w", err)
	}
	var darks []darkFrame
	if err := db.WithContext(ctx).Table("frames").
		Select(`key, exposure, gain, "offset", set_temp, bin_x, date_obs, night, light_leak, cal_check, cal_check_reason, cal_median_adu, dark_spread`).
		Where("type = ? AND index_error IS NULL", darkType).
		Scan(&darks).Error; err != nil {
		return DarkBacklog{}, fmt.Errorf("load darks for the dark backlog: %w", err)
	}
	sort.SliceStable(darks, func(i, j int) bool { return darks[i].at().Before(darks[j].at()) })
	return darkBacklog(lights, darks, sets, now), nil
}

func darkBacklog(lights []darkLightGroup, darks []darkFrame, sets []calmatch.Set, now time.Time) DarkBacklog {
	counts, measured := measureCleanSets(sets, darks)
	b := DarkBacklog{
		ComputedAt: now.UTC(), FramesNeeded: DarkFramesTarget, FramesNeededBasis: FramesNeededBasis(measured),
		SetTempExactC: calmatch.SetTempExactC, SessionGapHours: SessionGap.Hours(),
		Combos: []DarkCombo{}, Unschedulable: []DarkUnscheduled{}, Rejected: []RejectedDark{},
	}
	tallies := map[darkSetup]map[float64]*tempTally{}
	unsched := map[string]*DarkUnscheduled{}
	for _, l := range lights {
		reason := ""
		switch {
		case l.Exposure == nil || !(*l.Exposure > 0):
			reason = "the lights have no exposure time in their header"
		case l.Gain == nil || l.Offset == nil:
			reason = "the lights have no GAIN or OFFSET in their header"
		case l.SetTemp == nil:
			reason = "the lights have no SET-TEMP in their header"
		case l.BinX == nil:
			reason = "the lights have no XBINNING in their header"
		}
		if reason != "" {
			k := fmt.Sprintf("%v|%v|%s", fptr(l.Exposure), fptr(l.SetTemp), reason)
			u, ok := unsched[k]
			if !ok {
				u = &DarkUnscheduled{Exposure: l.Exposure, SetTemp: l.SetTemp, Reason: reason}
				unsched[k] = u
			}
			u.Lights += l.N
			continue
		}
		g := calmatch.Group{Night: l.Night, Exposure: *l.Exposure, Gain: *l.Gain, Offset: *l.Offset,
			SetTemp: *l.SetTemp, BinX: *l.BinX, Rotator: math.NaN()}
		m := calmatch.Choose(g, sets).Dark
		if m.Quality == calmatch.Exact && fullExactDark(g, sets, counts) {
			continue
		}
		su := darkSetup{exposure: g.Exposure, gain: g.Gain, offset: g.Offset, bin: g.BinX}
		if tallies[su] == nil {
			tallies[su] = map[float64]*tempTally{}
		}
		t := tallies[su][g.SetTemp]
		if t == nil {
			t = &tempTally{nights: map[string]bool{}, readout: map[string]int{}}
			tallies[su][g.SetTemp] = t
		}
		switch m.Quality {
		case calmatch.Missing:
			t.blocked += l.N
		case calmatch.Exact:
			t.thin += l.N
		case calmatch.Fallback:
			t.scaled += l.N
		}
		night := l.Night.Format(time.DateOnly)
		t.nights[night] = true
		if night > t.latest {
			t.latest = night
		}
		if l.ReadoutMode != nil && *l.ReadoutMode != "" {
			t.readout[*l.ReadoutMode] += l.N
		}
	}
	for _, u := range unsched {
		b.Unschedulable = append(b.Unschedulable, *u)
	}
	sort.SliceStable(b.Unschedulable, func(i, j int) bool { return b.Unschedulable[i].Lights > b.Unschedulable[j].Lights })

	for su, temps := range tallies {
		for _, c := range pickSetTemps(temps) {
			c.Exposure, c.Gain, c.Offset, c.Binning = su.exposure, su.gain, su.offset, su.bin
			c.ComboKey = ComboKey(su.exposure, su.gain, su.offset, su.bin, c.SetTemp)
			c.FramesNeeded = DarkFramesTarget
			fillDarkProgress(&c, su, darks, now)
			b.Combos = append(b.Combos, c)
		}
	}
	sort.SliceStable(b.Combos, func(i, j int) bool {
		a, c := b.Combos[i], b.Combos[j]
		if a.LightsBlocked != c.LightsBlocked {
			return a.LightsBlocked > c.LightsBlocked
		}
		if a.LightsScaled != c.LightsScaled {
			return a.LightsScaled > c.LightsScaled
		}
		if a.LightsThin != c.LightsThin {
			return a.LightsThin > c.LightsThin
		}
		if a.LatestNight != c.LatestNight {
			return a.LatestNight > c.LatestNight
		}
		return a.ComboKey < c.ComboKey
	})
	for i := range b.Combos {
		b.Combos[i].Priority = i + 1
	}
	b.LastSession, b.Rejected, b.Unchecked = darkHistory(darks)
	return b
}

func fptr(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func pickSetTemps(temps map[float64]*tempTally) []DarkCombo {
	left := map[float64]bool{}
	for t := range temps {
		left[t] = true
	}
	var out []DarkCombo
	for len(left) > 0 {
		var best float64
		bestBlocked, bestScaled, bestThin, found := -1, -1, -1, false
		for cand := range left {
			bl, sc, th := 0, 0, 0
			for t := range left {
				if math.Abs(t-cand) <= calmatch.SetTempExactC {
					bl += temps[t].blocked
					sc += temps[t].scaled
					th += temps[t].thin
				}
			}
			if !found || bl > bestBlocked || (bl == bestBlocked && (sc > bestScaled || (sc == bestScaled && (th > bestThin || (th == bestThin && cand > best))))) {
				best, bestBlocked, bestScaled, bestThin, found = cand, bl, sc, th, true
			}
		}
		c := DarkCombo{SetTemp: best, SetTempsCovered: []float64{}}
		nights := map[string]bool{}
		readout := map[string]int{}
		var covered []float64
		for t := range left {
			if math.Abs(t-best) > calmatch.SetTempExactC {
				continue
			}
			covered = append(covered, t)
		}
		sort.Float64s(covered)
		for _, t := range covered {
			tt := temps[t]
			c.LightsBlocked += tt.blocked
			c.LightsScaled += tt.scaled
			c.LightsThin += tt.thin
			for n := range tt.nights {
				nights[n] = true
			}
			if tt.latest > c.LatestNight {
				c.LatestNight = tt.latest
			}
			for r, n := range tt.readout {
				readout[r] += n
			}
			delete(left, t)
		}
		c.SetTempsCovered = covered
		c.Nights = len(nights)
		mode, most := "", 0
		for r, n := range readout {
			if n > most || (n == most && r < mode) {
				mode, most = r, n
			}
		}
		if mode != "" {
			c.ReadoutMode = &mode
		}
		temps := make([]string, len(covered))
		for i, t := range covered {
			temps[i] = numText(t)
		}
		c.Basis = fmt.Sprintf("%d lights at set temperature %s °C have no dark to calibrate with, %d use a dark scaled from another setpoint or exposure "+
			"and %d have an exact dark set with fewer than %d clean darks. "+
			"A dark at %s °C is an exact match for lights within %s °C of it (calmatch.SetTempExactC).",
			c.LightsBlocked, strings.Join(temps, ", "), c.LightsScaled, c.LightsThin, DarkFramesTarget, numText(best), numText(calmatch.SetTempExactC))
		out = append(out, c)
	}
	return out
}

func sameSetup(f darkFrame, su darkSetup, setTemp float64) bool {
	return f.Exposure != nil && f.Gain != nil && f.Offset != nil && f.SetTemp != nil &&
		math.Abs(*f.Exposure-su.exposure) <= calmatch.ExposureTolerance*su.exposure &&
		*f.Gain == su.gain && *f.Offset == su.offset && *f.SetTemp == setTemp && same(val(f.BinX), su.bin)
}

func fillDarkProgress(c *DarkCombo, su darkSetup, darks []darkFrame, now time.Time) {
	var last time.Time
	open := 0
	for _, f := range darks {
		if !sameSetup(f, su, c.SetTemp) {
			continue
		}
		at := f.at()
		if c.NewestDarkAt == nil || at.After(*c.NewestDarkAt) {
			a := at.UTC()
			c.NewestDarkAt = &a
		}
		if f.LightLeak != nil {
			if now.Sub(at) <= SessionGap {
				c.FramesRejected++
			}
			continue
		}
		if !last.IsZero() && at.Sub(last) > SessionGap {
			open = 0
		}
		last = at
		switch {
		case clean(f):
			open++
		case rejected(f) && now.Sub(at) <= SessionGap:
			c.FramesRejected++
		}
	}
	if !last.IsZero() && now.Sub(last) <= SessionGap {
		c.FramesHave = open
	}
}

func darkHistory(darks []darkFrame) (*DarkSession, []RejectedDark, int) {
	var s *DarkSession
	unchecked := 0
	rejected := []RejectedDark{}
	for _, f := range darks {
		at := f.at()
		if s == nil || at.Sub(s.To) > SessionGap {
			s = &DarkSession{From: at.UTC()}
		}
		s.To = at.UTC()
		s.Frames++
		switch {
		case f.LightLeak != nil:
			s.Leak++
		case f.CalCheck != nil && *f.CalCheck == darkcheck.StateClean:
			s.Clean++
		default:
			s.Unchecked++
		}
		if f.LightLeak == nil && (f.CalCheck == nil || *f.CalCheck == darkcheck.StateUnchecked) {
			unchecked++
		}
	}
	for i := len(darks) - 1; i >= 0 && len(rejected) < RejectedListed; i-- {
		f := darks[i]
		if f.LightLeak == nil {
			continue
		}
		r := RejectedDark{Key: f.Key, Exposure: f.Exposure, Gain: f.Gain, Offset: f.Offset, SetTemp: f.SetTemp,
			SpreadADU: f.DarkSpread, MedianADU: f.CalMedianADU}
		if at := f.at(); !at.IsZero() {
			a := at.UTC()
			r.TakenAt = &a
		}
		switch {
		case f.CalCheckReason != nil && f.CalCheck != nil && (*f.CalCheck == darkcheck.StateLeak || *f.CalCheck == darkcheck.StateOffTemp):
			r.Reason = *f.CalCheckReason
		default:
			r.Reason = fmt.Sprintf("left out when its master was built: large-scale spread %s ADU (checked before references were measured)", numText(math.Round(*f.LightLeak*100)/100))
		}
		rejected = append(rejected, r)
	}
	return s, rejected, unchecked
}
