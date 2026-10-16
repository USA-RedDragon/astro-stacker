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
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

type DarkBacklog struct {
	ComputedAt        time.Time         `json:"computed_at"`
	FramesNeeded      int               `json:"frames_needed"`
	FramesNeededLong  int               `json:"frames_needed_long"`
	LongExposureS     float64           `json:"long_exposure_s"`
	FramesNeededBasis string            `json:"frames_needed_basis"`
	SetTempExactC     float64           `json:"set_temp_exact_c"`
	SessionGapHours   float64           `json:"session_gap_hours"`
	RankingBasis      string            `json:"ranking_basis"`
	LastNight         string            `json:"last_night"`
	Combos            []DarkCombo       `json:"combos"`
	Unschedulable     []DarkUnscheduled `json:"unschedulable"`
	LastSession       *DarkSession      `json:"last_session"`
	Rejected          []RejectedDark    `json:"rejected"`
	RejectedTotal     int               `json:"rejected_total"`
	Unchecked         int               `json:"unchecked"`
}

type DarkCombo struct {
	ComboKey        string       `json:"combo_key"`
	Priority        int          `json:"priority"`
	Exposure        float64      `json:"exposure"`
	Gain            float64      `json:"gain"`
	Offset          float64      `json:"offset"`
	Binning         float64      `json:"binning"`
	ReadoutMode     *string      `json:"readout_mode"`
	SetTemp         float64      `json:"set_temp"`
	SetTempsCovered []float64    `json:"set_temps_covered"`
	FramesNeeded    int          `json:"frames_needed"`
	FramesHave      int          `json:"frames_have"`
	FramesRejected  int          `json:"frames_rejected"`
	LightsBlocked   int          `json:"lights_blocked"`
	LightsScaled    int          `json:"lights_scaled"`
	LightsThin      int          `json:"lights_thin"`
	LightsRecent    int          `json:"lights_recent"`
	LightsActive    int          `json:"lights_active"`
	LightsLastNight int          `json:"lights_last_night"`
	Score           float64      `json:"score"`
	ScoreParts      ScoreParts   `json:"score_parts"`
	Nights          int          `json:"nights"`
	LatestNight     string       `json:"latest_night"`
	NewestDarkAt    *time.Time   `json:"newest_dark_at"`
	Requests        []RequestRef `json:"requests"`
	Targets         []DarkTarget `json:"targets"`
	Basis           string       `json:"basis"`
}

// DarkTarget counts the lights of one object in a combo's backlog.
type DarkTarget struct {
	Name   string `json:"name"`
	Lights int    `json:"lights"`
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
	Flagged   int       `json:"flagged"`
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

// DarkFramesTarget is how many clean darks make a full set at this exposure.
func DarkFramesTarget(exposure float64) int { return calmatch.FullDarkSet(exposure) }

type cleanSets struct {
	median, sets int
}

func FramesNeededBasis(c cleanSets) string {
	measured := "No dark set on record has a clean dark yet."
	if c.sets > 0 {
		measured = fmt.Sprintf("The %d dark sets on record have a median of %d clean darks each (ingest check verdict clean, not rejected).", c.sets, c.median)
	}
	return fmt.Sprintf("A full set is %s, the user's choice. %s "+
		"Lights use a full set of the same exposure within %s °C when one exists. Only when none does is a shorter set of %d or more "+
		"(calmatch.MinFrames) used, and its lights stay on this backlog until a full set is taken.",
		calmatch.FullDarkRule(), measured, numText(calmatch.SetTempExactC), calmatch.MinFrames)
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

// fullChosenDark reports whether the dark set chosen for some lights is
// full: a master made elsewhere, or a set with DarkFramesTarget clean darks for its exposure.
func fullChosenDark(chosen calmatch.Set, sets []calmatch.Set, counts map[int]int) bool {
	if chosen.Master != "" {
		return true
	}
	for i, s := range sets {
		if s.Type == chosen.Type && s.From.Equal(chosen.From) && s.To.Equal(chosen.To) && s.Master == "" &&
			same(s.Exposure, chosen.Exposure) && same(s.Gain, chosen.Gain) && same(s.Offset, chosen.Offset) &&
			same(s.SetTemp, chosen.SetTemp) && same(s.BinX, chosen.BinX) {
			return counts[i] >= DarkFramesTarget(s.Exposure)
		}
	}
	return false
}

type darkLightGroup struct {
	Night       time.Time
	Object      string
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
	recent, active        int
	lastNight             int
	score                 ScoreParts
	nights                map[string]bool
	latest                string
	readout               map[string]int
	objects               map[string]int
}

func BuildDarkBacklog(ctx context.Context, db *gorm.DB, now time.Time) (DarkBacklog, error) {
	sets, _, err := loadSets(ctx, db)
	if err != nil {
		return DarkBacklog{}, err
	}
	var lights []darkLightGroup
	if err := db.WithContext(ctx).Table("frames").
		Select(`night, object, exposure, gain, "offset", set_temp, bin_x, readout_mode, COUNT(*) as n`).
		Where("type = ? AND night IS NOT NULL AND index_error IS NULL", "LIGHT").
		Group(`night, object, exposure, gain, "offset", set_temp, bin_x, readout_mode`).
		Scan(&lights).Error; err != nil {
		return DarkBacklog{}, fmt.Errorf("load lights for the dark backlog: %w", err)
	}
	darks, err := loadDarks(ctx, db)
	if err != nil {
		return DarkBacklog{}, err
	}
	b := darkBacklog(lights, darks, sets, now)
	needs, err := openNeeds(ctx, db, app.CalibrationDarks, requestFrames{darks: darks})
	if err != nil {
		return DarkBacklog{}, err
	}
	mergeDarkRequests(&b, needs, darks, now)
	return b, nil
}

func loadDarks(ctx context.Context, db *gorm.DB) ([]darkFrame, error) {
	var darks []darkFrame
	if err := db.WithContext(ctx).Table("frames").
		Select(`key, exposure, gain, "offset", set_temp, bin_x, date_obs, night, light_leak, cal_check, cal_check_reason, cal_median_adu, dark_spread`).
		Where("type = ? AND index_error IS NULL", darkType).
		Scan(&darks).Error; err != nil {
		return nil, fmt.Errorf("load darks for the dark backlog: %w", err)
	}
	sort.SliceStable(darks, func(i, j int) bool { return darks[i].at().Before(darks[j].at()) })
	return darks, nil
}

// RejectedDarks lists every dark left out of its master, newest first.
func RejectedDarks(ctx context.Context, db *gorm.DB) ([]RejectedDark, error) {
	darks, err := loadDarks(ctx, db)
	if err != nil {
		return nil, err
	}
	return rejectedDarks(darks, len(darks)), nil
}

func darkBacklog(lights []darkLightGroup, darks []darkFrame, sets []calmatch.Set, now time.Time) DarkBacklog {
	counts, measured := measureCleanSets(sets, darks)
	r := newDarkRank(lights, now)
	b := DarkBacklog{
		ComputedAt: now.UTC(), FramesNeeded: calmatch.ShortDarkSet, FramesNeededLong: calmatch.LongDarkSet, LongExposureS: calmatch.LongDarkSeconds,
		FramesNeededBasis: FramesNeededBasis(measured),
		SetTempExactC:     calmatch.SetTempExactC, SessionGapHours: SessionGap.Hours(), RankingBasis: RankingBasis(), LastNight: r.lastNightText(),
		Combos: []DarkCombo{}, Unschedulable: []DarkUnscheduled{}, Rejected: []RejectedDark{},
	}
	tallies := map[darkSetup]map[float64]*tempTally{}
	unsched := map[string]*DarkUnscheduled{}
	for _, l := range lights {
		reason := ""
		switch {
		case l.Exposure == nil || !(*l.Exposure > 0):
			reason = reasonNoExposure
		case l.Gain == nil || l.Offset == nil:
			reason = reasonNoGainOffset
		case l.SetTemp == nil:
			reason = reasonNoSetTemp
		case l.BinX == nil:
			reason = reasonNoBinning
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
		if m.Quality == calmatch.Exact && fullChosenDark(*m.Set, sets, counts) {
			continue
		}
		su := darkSetup{exposure: g.Exposure, gain: g.Gain, offset: g.Offset, bin: g.BinX}
		if tallies[su] == nil {
			tallies[su] = map[float64]*tempTally{}
		}
		t := tallies[su][g.SetTemp]
		if t == nil {
			t = &tempTally{nights: map[string]bool{}, readout: map[string]int{}, objects: map[string]int{}}
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
		r.add(t, l, m.Quality)
		t.addLights(l)
	}
	for _, u := range unsched {
		b.Unschedulable = append(b.Unschedulable, *u)
	}
	sort.SliceStable(b.Unschedulable, func(i, j int) bool { return b.Unschedulable[i].Lights > b.Unschedulable[j].Lights })

	for su, temps := range tallies {
		for _, c := range pickSetTemps(temps, r.lastNightText(), DarkFramesTarget(su.exposure)) {
			c.Exposure, c.Gain, c.Offset, c.Binning = su.exposure, su.gain, su.offset, su.bin
			c.ComboKey = ComboKey(su.exposure, su.gain, su.offset, su.bin, c.SetTemp)
			c.FramesNeeded = DarkFramesTarget(su.exposure)
			fillDarkProgress(&c, su, darks, now)
			b.Combos = append(b.Combos, c)
		}
	}
	sort.SliceStable(b.Combos, func(i, j int) bool {
		a, c := b.Combos[i], b.Combos[j]
		if (a.LightsLastNight > 0) != (c.LightsLastNight > 0) {
			return a.LightsLastNight > 0
		}
		if a.Score != c.Score {
			return a.Score > c.Score
		}
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
	b.RejectedTotal = rejectedCount(darks)
	return b
}

func (t *tempTally) addLights(l darkLightGroup) {
	night := l.Night.Format(time.DateOnly)
	t.nights[night] = true
	if night > t.latest {
		t.latest = night
	}
	if l.ReadoutMode != nil && *l.ReadoutMode != "" {
		t.readout[*l.ReadoutMode] += l.N
	}
	if l.Object != "" {
		t.objects[l.Object] += l.N
	}
}

func fptr(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func pickSetTemps(temps map[float64]*tempTally, lastNight string, need int) []DarkCombo {
	left := map[float64]bool{}
	for t := range temps {
		left[t] = true
	}
	var out []DarkCombo
	for len(left) > 0 {
		var best, bestScore float64
		bestLast, bestBlocked, bestScaled, bestThin, found := -1, -1, -1, -1, false
		for cand := range left {
			last, bl, sc, th, score := 0, 0, 0, 0, 0.0
			for t := range left {
				if math.Abs(t-cand) <= calmatch.SetTempExactC {
					last += temps[t].lastNight
					bl += temps[t].blocked
					sc += temps[t].scaled
					th += temps[t].thin
					score += temps[t].score.total()
				}
			}
			score = math.Round(score*1e6) / 1e6
			if !found || last > bestLast || (last == bestLast && (score > bestScore || (score == bestScore && (bl > bestBlocked || (bl == bestBlocked &&
				(sc > bestScaled || (sc == bestScaled && (th > bestThin || (th == bestThin && cand > best))))))))) {
				best, bestLast, bestScore, bestBlocked, bestScaled, bestThin, found = cand, last, score, bl, sc, th, true
			}
		}
		c := DarkCombo{SetTemp: best, SetTempsCovered: []float64{}, Requests: []RequestRef{}}
		nights := map[string]bool{}
		readout := map[string]int{}
		objects := map[string]int{}
		var covered []float64
		for t := range left {
			if math.Abs(t-best) > calmatch.SetTempExactC {
				continue
			}
			covered = append(covered, t)
		}
		sort.Float64s(covered)
		for _, t := range covered {
			c.addTally(temps[t], nights, readout, objects)
			delete(left, t)
		}
		c.SetTempsCovered = covered
		c.Targets = darkTargets(objects)
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
			c.LightsBlocked, strings.Join(temps, ", "), c.LightsScaled, c.LightsThin, need, numText(best), numText(calmatch.SetTempExactC))
		c.ScoreParts = c.ScoreParts.rounded()
		c.Score = roundScore(c.ScoreParts.Blocked + c.ScoreParts.Scaled + c.ScoreParts.Thin)
		c.Basis += " " + scoreNote(c, lastNight)
		out = append(out, c)
	}
	return out
}

func (c *DarkCombo) addTally(tt *tempTally, nights map[string]bool, readout, objects map[string]int) {
	c.LightsBlocked += tt.blocked
	c.LightsScaled += tt.scaled
	c.LightsThin += tt.thin
	c.LightsRecent += tt.recent
	c.LightsActive += tt.active
	c.LightsLastNight += tt.lastNight
	c.ScoreParts.Blocked += tt.score.Blocked
	c.ScoreParts.Scaled += tt.score.Scaled
	c.ScoreParts.Thin += tt.score.Thin
	for n := range tt.nights {
		nights[n] = true
	}
	if tt.latest > c.LatestNight {
		c.LatestNight = tt.latest
	}
	for r, n := range tt.readout {
		readout[r] += n
	}
	for o, n := range tt.objects {
		objects[o] += n
	}
}

func darkTargets(objects map[string]int) []DarkTarget {
	out := make([]DarkTarget, 0, len(objects))
	for name, n := range objects {
		out = append(out, DarkTarget{Name: name, Lights: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lights != out[j].Lights {
			return out[i].Lights > out[j].Lights
		}
		return out[i].Name < out[j].Name
	})
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
		case f.CalCheck != nil && (*f.CalCheck == darkcheck.StateLeak || *f.CalCheck == darkcheck.StateOffTemp):
			s.Flagged++
		default:
			s.Unchecked++
		}
		if f.LightLeak == nil && (f.CalCheck == nil || *f.CalCheck == darkcheck.StateUnchecked) {
			unchecked++
		}
	}
	return s, rejectedDarks(darks, RejectedListed), unchecked
}

func rejectedCount(darks []darkFrame) int {
	n := 0
	for _, f := range darks {
		if f.LightLeak != nil {
			n++
		}
	}
	return n
}

func rejectedDarks(darks []darkFrame, limit int) []RejectedDark {
	rejected := []RejectedDark{}
	for i := len(darks) - 1; i >= 0 && len(rejected) < limit; i-- {
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
	return rejected
}
