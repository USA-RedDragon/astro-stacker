package coverage

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

// RequestItem is one set a calibration request asks for, with the frames
// of it indexed since the request was made.
type RequestItem struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	FramesHave   int    `json:"frames_have"`
	FramesNeeded int    `json:"frames_needed"`
	Done         bool   `json:"done"`
}

// RequestProgress is what a request asks for and how far it got. Lights
// counts the object's lights of the night; Unschedulable says why none of
// them can be matched with the requested type when Items is empty.
type RequestProgress struct {
	Items         []RequestItem
	Lights        int
	Unschedulable string
	needs         []requestNeed
}

// Done reports whether every set the request asks for is indexed.
func (p RequestProgress) Done() bool {
	if len(p.Items) == 0 {
		return false
	}
	for _, it := range p.Items {
		if !it.Done {
			return false
		}
	}
	return true
}

// Filters lists the filters of a flats request's lights.
func (p RequestProgress) Filters() []string {
	out := make([]string, 0, len(p.needs))
	for _, n := range p.needs {
		if n.filter != "" {
			out = append(out, n.filter)
		}
	}
	return out
}

// RequestBasis says what counts as done for a request type, and why.
func RequestBasis(typ string) string {
	switch typ {
	case app.CalibrationFlats:
		return fmt.Sprintf("Done when %d flats (calmatch.MinFrames, the fewest a master is built from) of each filter of the night's lights, "+
			"named for the object, are indexed with a time at or after the request.", calmatch.MinFrames)
	case app.CalibrationDarks:
		return fmt.Sprintf("Done when a full set of darks (%s) of each exposure, gain, offset, binning and set temperature of the night's lights, "+
			"within %s °C (calmatch.SetTempExactC), are indexed with a time at or after the request and not rejected by the ingest check.",
			calmatch.FullDarkRule(), numText(calmatch.SetTempExactC))
	case app.CalibrationBias:
		return fmt.Sprintf("Done when %d bias frames (the bias set target) of each gain, offset and binning of the night's lights are indexed "+
			"with a time at or after the request.", BiasFramesTarget)
	}
	return ""
}

type requestNeed struct {
	key, label, filter               string
	exposure, gain, offset, bin, tmp float64
	readout                          *string
	needed                           int
	have                             int
}

type requestLight struct {
	Night       time.Time
	Filter      string
	Exposure    *float64
	Gain        *float64
	Offset      *float64
	SetTemp     *float64
	BinX        *float64
	ReadoutMode *string
	N           int
}

type flatFrame struct {
	Object  string
	Filter  string
	DateObs *time.Time
	Night   *time.Time
}

// requestFrames is what measuring requests needs from the frames table,
// loaded once for all of them.
type requestFrames struct {
	darks []darkFrame
	bias  []biasFrame
	flats []flatFrame
}

func loadRequestFrames(ctx context.Context, db *gorm.DB, reqs []app.CalibrationRequest) (requestFrames, error) {
	var out requestFrames
	var flatObjects []string
	need := map[string]bool{}
	for _, r := range reqs {
		need[r.Type] = true
		if r.Type == app.CalibrationFlats {
			flatObjects = append(flatObjects, r.Object)
		}
	}
	var err error
	if need[app.CalibrationDarks] {
		if out.darks, err = loadDarks(ctx, db); err != nil {
			return out, err
		}
	}
	if need[app.CalibrationBias] {
		if out.bias, err = loadBias(ctx, db); err != nil {
			return out, err
		}
	}
	if len(flatObjects) > 0 {
		if err := db.WithContext(ctx).Table("frames").Select("object, filter, date_obs, night").
			Where("type = ? AND index_error IS NULL AND object IN ?", "FLAT", flatObjects).
			Scan(&out.flats).Error; err != nil {
			return out, fmt.Errorf("load flats for calibration requests: %w", err)
		}
	}
	return out, nil
}

func loadRequestLights(ctx context.Context, db *gorm.DB, object string, night time.Time) ([]requestLight, error) {
	var rows []requestLight
	if err := db.WithContext(ctx).Table("frames").
		Select(`night, filter, exposure, gain, "offset", set_temp, bin_x, readout_mode, COUNT(*) as n`).
		Where("type = ? AND object = ? AND night IS NOT NULL AND index_error IS NULL", "LIGHT", object).
		Group(`night, filter, exposure, gain, "offset", set_temp, bin_x, readout_mode`).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load lights of %s: %w", object, err)
	}
	day := night.Format(time.DateOnly)
	out := rows[:0]
	for _, r := range rows {
		if r.Night.Format(time.DateOnly) == day {
			out = append(out, r)
		}
	}
	return out, nil
}

type needTally struct {
	need    requestNeed
	readout map[string]int
}

// requestNeeds groups a night's lights into the sets a request of typ asks
// for. With none, it says why.
func requestNeeds(typ string, lights []requestLight) ([]requestNeed, string) {
	tallies := map[string]*needTally{}
	reason := ""
	for _, l := range lights {
		n, why := needFor(typ, l)
		if why != "" {
			if reason == "" {
				reason = why
			}
			continue
		}
		t := tallies[n.key]
		if t == nil {
			t = &needTally{need: n, readout: map[string]int{}}
			tallies[n.key] = t
		}
		if l.ReadoutMode != nil && *l.ReadoutMode != "" {
			t.readout[*l.ReadoutMode] += l.N
		}
	}
	out := make([]requestNeed, 0, len(tallies))
	for _, t := range tallies {
		t.need.readout = mostCommon(t.readout)
		out = append(out, t.need)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	if len(out) > 0 {
		reason = ""
	}
	return out, reason
}

func needFor(typ string, l requestLight) (requestNeed, string) {
	switch typ {
	case app.CalibrationFlats:
		if l.Filter == "" {
			return requestNeed{}, "the lights have no FILTER in their header"
		}
		return requestNeed{key: l.Filter, label: l.Filter + " flats", filter: l.Filter, needed: calmatch.MinFrames}, ""
	case app.CalibrationDarks:
		switch {
		case l.Exposure == nil || !(*l.Exposure > 0):
			return requestNeed{}, reasonNoExposure
		case l.Gain == nil || l.Offset == nil:
			return requestNeed{}, reasonNoGainOffset
		case l.SetTemp == nil:
			return requestNeed{}, reasonNoSetTemp
		case l.BinX == nil:
			return requestNeed{}, reasonNoBinning
		}
		n := requestNeed{exposure: *l.Exposure, gain: *l.Gain, offset: *l.Offset, bin: *l.BinX, tmp: *l.SetTemp, needed: DarkFramesTarget(*l.Exposure)}
		n.key = ComboKey(n.exposure, n.gain, n.offset, n.bin, n.tmp)
		n.label = fmt.Sprintf("%s s darks at %s °C, gain %s, offset %s, bin %s", numText(n.exposure), numText(n.tmp), numText(n.gain),
			numText(n.offset), numText(n.bin))
		return n, ""
	case app.CalibrationBias:
		switch {
		case l.Gain == nil || l.Offset == nil:
			return requestNeed{}, reasonNoGainOffset
		case l.BinX == nil:
			return requestNeed{}, reasonNoBinning
		}
		n := requestNeed{gain: *l.Gain, offset: *l.Offset, bin: *l.BinX, needed: BiasFramesTarget}
		n.key = BiasComboKey(n.gain, n.offset, n.bin)
		n.label = fmt.Sprintf("Bias at gain %s, offset %s, bin %s", numText(n.gain), numText(n.offset), numText(n.bin))
		return n, ""
	}
	return requestNeed{}, "unknown calibration type " + typ
}

// darkCounts reports whether a dark counts toward a requested dark set: the
// same setup within SetTempExactC, and not rejected by the ingest check.
func (n requestNeed) darkCounts(f darkFrame) bool {
	return f.Exposure != nil && f.Gain != nil && f.Offset != nil && f.SetTemp != nil &&
		math.Abs(*f.Exposure-n.exposure) <= calmatch.ExposureTolerance*n.exposure && *f.Gain == n.gain && *f.Offset == n.offset &&
		same(val(f.BinX), n.bin) && math.Abs(*f.SetTemp-n.tmp) <= calmatch.SetTempExactC && f.LightLeak == nil && !rejected(f)
}

func (n *requestNeed) measure(typ, object string, since time.Time, fr requestFrames) {
	n.have = 0
	switch typ {
	case app.CalibrationFlats:
		for _, f := range fr.flats {
			if f.Object == object && f.Filter == n.filter && !takenAt(f.DateObs, val0(f.Night)).Before(since) {
				n.have++
			}
		}
	case app.CalibrationDarks:
		for _, f := range fr.darks {
			if n.darkCounts(f) && !f.at().Before(since) {
				n.have++
			}
		}
	case app.CalibrationBias:
		su := biasSetup{gain: n.gain, offset: n.offset, bin: n.bin}
		for _, f := range fr.bias {
			if su.matches(f) && !f.at().Before(since) {
				n.have++
			}
		}
	}
}

func progressOf(r app.CalibrationRequest, lights []requestLight, fr requestFrames) RequestProgress {
	p := RequestProgress{Items: []RequestItem{}}
	for _, l := range lights {
		p.Lights += l.N
	}
	p.needs, p.Unschedulable = requestNeeds(r.Type, lights)
	for i := range p.needs {
		n := &p.needs[i]
		n.measure(r.Type, r.Object, r.CreatedAt, fr)
		p.Items = append(p.Items, RequestItem{Key: n.key, Label: n.label, FramesHave: n.have, FramesNeeded: n.needed, Done: n.have >= n.needed})
	}
	return p
}

// MeasureRequests measures each request's progress from the frames index.
func MeasureRequests(ctx context.Context, db *gorm.DB, reqs []app.CalibrationRequest) ([]RequestProgress, error) {
	fr, err := loadRequestFrames(ctx, db, reqs)
	if err != nil {
		return nil, err
	}
	out := make([]RequestProgress, 0, len(reqs))
	for _, r := range reqs {
		lights, err := loadRequestLights(ctx, db, r.Object, r.Night)
		if err != nil {
			return nil, err
		}
		out = append(out, progressOf(r, lights, fr))
	}
	return out, nil
}

// RequestLights is what a new request of typ for object's lights of night
// would ask for.
func RequestLights(ctx context.Context, db *gorm.DB, typ, object string, night time.Time) (RequestProgress, error) {
	lights, err := loadRequestLights(ctx, db, object, night)
	if err != nil {
		return RequestProgress{}, err
	}
	return progressOf(app.CalibrationRequest{Type: typ, Object: object, Night: night}, lights, requestFrames{}), nil
}

type openNeed struct {
	ref  RequestRef
	need requestNeed
}

// openNeeds lists the sets open requests of typ still need, with the frames
// of each taken since its request. None when the requests table is missing.
func openNeeds(ctx context.Context, db *gorm.DB, typ string, fr requestFrames) ([]openNeed, error) {
	has, err := tables.Has(ctx, db, &app.CalibrationRequest{})
	if err != nil || !has {
		return nil, err
	}
	var reqs []app.CalibrationRequest
	if err := db.WithContext(ctx).Where("type = ? AND status = ?", typ, app.CalibrationOpen).Order("created_at, id").
		Find(&reqs).Error; err != nil {
		return nil, fmt.Errorf("load open %s requests: %w", typ, err)
	}
	var out []openNeed
	for _, r := range reqs {
		lights, err := loadRequestLights(ctx, db, r.Object, r.Night)
		if err != nil {
			return nil, err
		}
		ref := RequestRef{ID: r.ID, Object: r.Object, Night: r.Night.Format(time.DateOnly)}
		for _, n := range progressOf(r, lights, fr).needs {
			if n.have < n.needed {
				out = append(out, openNeed{ref: ref, need: n})
			}
		}
	}
	return out, nil
}

func requestedNote(ref RequestRef) string {
	return fmt.Sprintf(" Requested for %s, night of %s.", ref.Object, ref.Night)
}

// requestedFirst puts requested combos first at priority 0 and numbers the
// rest from 1 in their order.
func requestedFirst[C any](cs []C, requested func(C) bool, setPriority func(*C, int)) {
	sort.SliceStable(cs, func(i, j int) bool { return requested(cs[i]) && !requested(cs[j]) })
	n := 0
	for i := range cs {
		if requested(cs[i]) {
			setPriority(&cs[i], 0)
			continue
		}
		n++
		setPriority(&cs[i], n)
	}
}

// mergeDarkRequests adds requested dark sets to the backlog: into the combo
// of the same setup that covers the request's set temperature, else into a
// requested combo within SetTempExactC of it, else as a new combo.
func mergeDarkRequests(b *DarkBacklog, needs []openNeed, darks []darkFrame, now time.Time) {
	for _, on := range needs {
		n := on.need
		sameSetup := func(c DarkCombo) bool {
			return c.Exposure == n.exposure && c.Gain == n.gain && c.Offset == n.offset && c.Binning == n.bin
		}
		idx := -1
		for i, c := range b.Combos {
			if sameSetup(c) && slices.Contains(c.SetTempsCovered, n.tmp) {
				idx = i
				break
			}
		}
		if idx < 0 {
			for i, c := range b.Combos {
				if len(c.Requests) > 0 && sameSetup(c) && math.Abs(c.SetTemp-n.tmp) <= calmatch.SetTempExactC {
					idx = i
					b.Combos[i].SetTempsCovered = append(b.Combos[i].SetTempsCovered, n.tmp)
					sort.Float64s(b.Combos[i].SetTempsCovered)
					break
				}
			}
		}
		if idx < 0 {
			c := DarkCombo{
				ComboKey: ComboKey(n.exposure, n.gain, n.offset, n.bin, n.tmp), Exposure: n.exposure, Gain: n.gain, Offset: n.offset,
				Binning: n.bin, ReadoutMode: n.readout, SetTemp: n.tmp, SetTempsCovered: []float64{n.tmp}, FramesNeeded: n.needed,
				Nights: 1, LatestNight: on.ref.Night, Requests: []RequestRef{}, Targets: []DarkTarget{},
				Basis: fmt.Sprintf("No lights at set temperature %s °C are short of darks; this set was asked for.", numText(n.tmp)),
			}
			fillDarkProgress(&c, darkSetup{exposure: n.exposure, gain: n.gain, offset: n.offset, bin: n.bin}, darks, now)
			b.Combos = append(b.Combos, c)
			idx = len(b.Combos) - 1
		}
		c := &b.Combos[idx]
		if !slices.ContainsFunc(c.Requests, func(r RequestRef) bool { return r.ID == on.ref.ID }) {
			c.Requests = append(c.Requests, on.ref)
			c.Basis += requestedNote(on.ref)
		}
		c.FramesHave = min(c.FramesHave, n.have)
	}
	requestedFirst(b.Combos, func(c DarkCombo) bool { return len(c.Requests) > 0 }, func(c *DarkCombo, p int) { c.Priority = p })
}

// mergeBiasRequests adds requested bias sets to the backlog, into the combo
// of the same gain, offset and binning or as a new one.
func mergeBiasRequests(b *BiasBacklog, needs []openNeed, frames []biasFrame, sets []calmatch.Set, now time.Time) {
	for _, on := range needs {
		n := on.need
		key := BiasComboKey(n.gain, n.offset, n.bin)
		idx := slices.IndexFunc(b.Combos, func(c BiasCombo) bool { return c.ComboKey == key })
		if idx < 0 {
			su := biasSetup{gain: n.gain, offset: n.offset, bin: n.bin}
			c := newBiasCombo(su, n.readout)
			c.Nights, c.LatestNight = 1, on.ref.Night
			c.Basis = fmt.Sprintf("No lights at gain %s, offset %s, bin %s are short of bias; this set was asked for.",
				numText(n.gain), numText(n.offset), numText(n.bin))
			fillBiasProgress(&c, su, frames, sets, now)
			b.Combos = append(b.Combos, c)
			idx = len(b.Combos) - 1
		}
		c := &b.Combos[idx]
		if !slices.ContainsFunc(c.Requests, func(r RequestRef) bool { return r.ID == on.ref.ID }) {
			c.Requests = append(c.Requests, on.ref)
			c.Basis += requestedNote(on.ref)
		}
		c.FramesHave = min(c.FramesHave, n.have)
	}
	requestedFirst(b.Combos, func(c BiasCombo) bool { return len(c.Requests) > 0 }, func(c *BiasCombo, p int) { c.Priority = p })
}
