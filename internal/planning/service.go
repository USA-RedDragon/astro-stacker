package planning

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

var Priorities = []string{"Low", "Normal", "High"}
var States = []string{"Draft", "Active", "Inactive", "Closed"}

type Rule struct {
	Name          string  `json:"name"`
	DefaultWeight float64 `json:"defaultWeight"`
	New           bool    `json:"new"`
	Description   string  `json:"description"`
}

func Rules() []Rule {
	return []Rule{
		{Name: "Project Priority", DefaultWeight: 50, Description: "High 1.0, Normal 0.5, Low 0."},
		{Name: "Setting Soonest", DefaultWeight: 50, Description: "Higher the sooner the target sets tonight."},
		{Name: "Percent Complete", DefaultWeight: 50, Description: "Fraction of the goal already reached."},
		{Name: "Target Switch Penalty", DefaultWeight: 67, Description: "1.0 for the target already running."},
		{Name: "Mosaic Completion", DefaultWeight: 0, Description: "Stock rule: favours a panel behind the mosaic's average."},
		{Name: "Panel Deficit", DefaultWeight: 0, Description: "Steers time to the panel and filter furthest behind its goal."},
		{Name: "Meridian Window Priority", DefaultWeight: 75, Description: "Favours targets inside their meridian window."},
		{Name: "Meridian Flip Penalty", DefaultWeight: 0, Description: "Avoids targets about to need a flip."},
		{Name: "Smart Exposure Order", DefaultWeight: 0, Description: "Favours filters that suit tonight's moon."},
		{Name: "Novelty", DefaultWeight: 10, New: true, Description: "Favours targets with little data over deepening ones that already have plenty. 1.0 with no subs, falling to 0 as the weakest filter reaches its goal."},
		{Name: "Rarity", DefaultWeight: 20, New: true, Description: "Favours targets with a short or closing season. 1.0 with 10 or fewer usable nights left, 0 with 120 or more."},
	}
}

type RuleWeight struct {
	Name    string  `json:"name"`
	Weight  float64 `json:"weight"`
	Missing bool    `json:"missing"`
}

type Plan struct {
	ID             int     `json:"id"`
	GUID           string  `json:"guid"`
	TemplateID     int     `json:"templateId"`
	Template       string  `json:"template"`
	Filter         string  `json:"filter"`
	Exposure       float64 `json:"exposure"`
	ExposureRaw    float64 `json:"exposureRaw"`
	Desired        int     `json:"desired"`
	Acquired       int     `json:"acquired"`
	Accepted       int     `json:"accepted"`
	Enabled        bool    `json:"enabled"`
	Gain           *int    `json:"gain"`
	MoonSeparation float64 `json:"moonSeparation"`
	MoonWidth      int     `json:"moonWidth"`
}

type FilterGoal struct {
	Filter     string          `json:"filter"`
	StackKey   string          `json:"stackFilter"`
	Goal       goals.Goal      `json:"goal"`
	GoalSet    bool            `json:"goalSet"`
	Measured   bool            `json:"measured"`
	Progress   *goals.Progress `json:"progress,omitempty"`
	Error      string          `json:"error,omitempty"`
	Accepted   int             `json:"accepted"`
	Desired    int             `json:"desired"`
	ExposureHr float64         `json:"acceptedHours"`
}

type Season struct {
	NightsLeft  int    `json:"nightsLeft"`
	OutOfSeason bool   `json:"outOfSeason"`
	SeasonEnd   string `json:"seasonEnd,omitempty"`
	ComputedFor string `json:"computedFor,omitempty"`
}

type Target struct {
	ID       int          `json:"id"`
	GUID     string       `json:"guid"`
	Name     string       `json:"name"`
	Active   bool         `json:"active"`
	RAHours  *float64     `json:"raHours"`
	Dec      *float64     `json:"dec"`
	Rotation float64      `json:"rotation"`
	Panel    int          `json:"panel,omitempty"`
	Plans    []Plan       `json:"plans"`
	Goals    []FilterGoal `json:"goals"`
	Weakest  *FilterGoal  `json:"weakest,omitempty"`
	Progress float64      `json:"progress"`
	EffHours float64      `json:"effectiveHours"`
	Season   *Season      `json:"season,omitempty"`
	Novelty  float64      `json:"novelty"`
	Rarity   float64      `json:"rarity"`
	LastSub  *time.Time   `json:"lastSub,omitempty"`
	SetName  string       `json:"exposureSet"`
	GoalMode goals.Kind   `json:"goalMode"`
}

type Project struct {
	ID              int          `json:"id"`
	GUID            string       `json:"guid"`
	Name            string       `json:"name"`
	Description     string       `json:"description"`
	State           string       `json:"state"`
	Priority        string       `json:"priority"`
	MinimumTime     int          `json:"minimumTime"`
	MinimumAltitude float64      `json:"minimumAltitude"`
	IsMosaic        bool         `json:"isMosaic"`
	Targets         []Target     `json:"targets"`
	SetName         string       `json:"exposureSet"`
	Progress        float64      `json:"progress"`
	WeakestTarget   string       `json:"weakestTarget,omitempty"`
	Weakest         *FilterGoal  `json:"weakest,omitempty"`
	Season          *Season      `json:"season,omitempty"`
	Novelty         float64      `json:"novelty"`
	Rarity          float64      `json:"rarity"`
	LastSub         *time.Time   `json:"lastSub,omitempty"`
	RuleWeights     []RuleWeight `json:"ruleWeights"`
	GoalDriven      bool         `json:"goalDriven"`
}

type Template struct {
	ID              int     `json:"id"`
	GUID            string  `json:"guid"`
	Name            string  `json:"name"`
	Filter          string  `json:"filter"`
	DefaultExposure float64 `json:"defaultExposure"`
	Gain            *int    `json:"gain"`
	Offset          *int    `json:"offset"`
	Bin             *int    `json:"bin"`
	TwilightLevel   string  `json:"twilight"`
	MoonEnabled     bool    `json:"moonEnabled"`
	MoonSeparation  float64 `json:"moonSeparation"`
	MoonWidth       int     `json:"moonWidth"`
	MoonDown        bool    `json:"moonDown"`
	MaximumHumidity float64 `json:"maximumHumidity"`
	UsedByPlans     int     `json:"usedByPlans"`
	UsedByTargets   int     `json:"usedByTargets"`
}

type Inputs struct {
	Goals       map[goals.Key]goals.Goal
	GoalFilters map[goals.Key]string
	Now         time.Time
}

type Snapshot struct {
	Projects  []Project     `json:"projects"`
	Templates []Template    `json:"templates"`
	Sets      []ExposureSet `json:"sets"`
	Rules     []Rule        `json:"rules"`
}

func twilightName(v *int) string {
	if v == nil {
		return "Nighttime"
	}
	switch *v {
	case 0:
		return "Nighttime"
	case 1:
		return "Astronomical"
	case 2:
		return "Nautical"
	case 3:
		return "Civil"
	}
	return "Nighttime"
}

func label(list []string, i int) string {
	if i >= 0 && i < len(list) {
		return list[i]
	}
	return "Unknown"
}

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

func Novelty(progress, effHours float64) float64 {
	s := math.Max(0, math.Min(1, 1-progress))
	if effHours >= 1 {
		s *= 0.8
	}
	return s
}

func Rarity(s *Season) float64 {
	if s == nil || s.OutOfSeason {
		return 0
	}
	return math.Max(0, math.Min(1, float64(120-s.NightsLeft)/110))
}

func tableExists(db *gorm.DB, name string) bool {
	return db.Migrator().HasTable(name)
}

func Load(ctx context.Context, sched, appDB *gorm.DB, in Inputs) (*Snapshot, error) {
	if sched == nil {
		return nil, errors.New("no scheduler database")
	}
	db := sched.WithContext(ctx)
	var projects []projectRow
	if err := db.Order(`"Id"`).Find(&projects).Error; err != nil {
		return nil, err
	}
	var targets []targetRow
	if err := db.Order(`"Id"`).Find(&targets).Error; err != nil {
		return nil, err
	}
	var plans []planRow
	if err := db.Order(`"Id"`).Find(&plans).Error; err != nil {
		return nil, err
	}
	var templates []templateRow
	if err := db.Order(`"Id"`).Find(&templates).Error; err != nil {
		return nil, err
	}
	var weights []ruleWeightRow
	if err := db.Find(&weights).Error; err != nil {
		return nil, err
	}
	seasons := map[string]Season{}
	if tableExists(db, "ts_target_season") {
		var rows []seasonRow
		if err := db.Find(&rows).Error; err == nil {
			for _, r := range rows {
				seasons[r.TargetGUID] = Season{NightsLeft: r.NightsLeft, OutOfSeason: r.OutOfSeason != 0, SeasonEnd: deref(r.SeasonEnd), ComputedFor: deref(r.ComputedFor)}
			}
		}
	}
	type lastRow struct {
		TargetID int   `gorm:"column:target_id"`
		Last     int64 `gorm:"column:last"`
	}
	var lasts []lastRow
	_ = db.Raw(`SELECT "targetId" AS target_id, max(acquireddate) AS last FROM acquiredimage GROUP BY "targetId"`).Scan(&lasts).Error
	lastBy := map[int]time.Time{}
	for _, l := range lasts {
		if l.Last > 0 {
			lastBy[l.TargetID] = fromTicks(l.Last)
		}
	}
	var measurements []app.GoalMeasurement
	if appDB != nil {
		if err := appDB.WithContext(ctx).Find(&measurements).Error; err != nil {
			return nil, err
		}
	}
	meas := map[goals.Key]app.GoalMeasurement{}
	for _, m := range measurements {
		meas[goals.Key{Object: m.Object, Filter: m.Filter}] = m
	}

	tmplBy := map[int]templateRow{}
	for _, t := range templates {
		tmplBy[t.ID] = t
	}
	plansBy := map[int][]planRow{}
	usedPlans := map[int]int{}
	usedTargets := map[int]map[int]bool{}
	for _, p := range plans {
		if p.TargetID == nil {
			continue
		}
		plansBy[*p.TargetID] = append(plansBy[*p.TargetID], p)
		if p.TemplateID != nil {
			usedPlans[*p.TemplateID]++
			if usedTargets[*p.TemplateID] == nil {
				usedTargets[*p.TemplateID] = map[int]bool{}
			}
			usedTargets[*p.TemplateID][*p.TargetID] = true
		}
	}
	targetsBy := map[int][]targetRow{}
	for _, t := range targets {
		if t.ProjectID != nil {
			targetsBy[*t.ProjectID] = append(targetsBy[*t.ProjectID], t)
		}
	}
	weightsBy := map[int]map[string]float64{}
	for _, w := range weights {
		if w.ProjectID == nil {
			continue
		}
		if weightsBy[*w.ProjectID] == nil {
			weightsBy[*w.ProjectID] = map[string]float64{}
		}
		weightsBy[*w.ProjectID][w.Name] = w.Weight
	}

	out := &Snapshot{Sets: ExposureSets(), Rules: Rules()}
	for _, pr := range projects {
		p := Project{
			ID: pr.ID, GUID: deref(pr.GUID), Name: pr.Name, Description: deref(pr.Description),
			State: label(States, pr.State), Priority: label(Priorities, pr.Priority),
			MinimumTime: pr.MinimumTime, MinimumAltitude: pr.MinimumAltitude, IsMosaic: pr.IsMosaic != 0,
			Progress: 1,
		}
		for _, r := range Rules() {
			w, ok := weightsBy[pr.ID][r.Name]
			p.RuleWeights = append(p.RuleWeights, RuleWeight{Name: r.Name, Weight: w, Missing: !ok})
		}
		var projSets []string
		var bestSeason *Season
		anySeason := false
		for _, tr := range targetsBy[pr.ID] {
			t := buildTarget(tr, plansBy[tr.ID], tmplBy, meas, in)
			if s, ok := seasons[t.GUID]; ok && t.GUID != "" {
				s := s
				t.Season = &s
				anySeason = true
				if !s.OutOfSeason && (bestSeason == nil || s.NightsLeft < bestSeason.NightsLeft) {
					bestSeason = &s
				}
			}
			t.Rarity = Rarity(t.Season)
			if l, ok := lastBy[tr.ID]; ok {
				l := l
				t.LastSub = &l
				if p.LastSub == nil || l.After(*p.LastSub) {
					p.LastSub = &l
				}
			}
			if t.Active && t.Progress < p.Progress {
				p.Progress = t.Progress
				p.WeakestTarget = t.Name
				p.Weakest = t.Weakest
				p.Novelty = t.Novelty
			}
			for _, g := range t.Goals {
				if g.GoalSet {
					p.GoalDriven = true
				}
			}
			projSets = append(projSets, t.SetName)
			p.Targets = append(p.Targets, t)
		}
		if p.Progress == 1 && p.Weakest == nil && len(p.Targets) > 0 {
			p.Novelty = p.Targets[0].Novelty
		}
		if len(p.Targets) == 0 {
			p.Progress = 0
			p.Novelty = 1
		}
		if bestSeason != nil {
			p.Season = bestSeason
		} else if anySeason {
			p.Season = &Season{OutOfSeason: true}
		}
		p.Rarity = Rarity(p.Season)
		p.SetName = commonSet(projSets)
		out.Projects = append(out.Projects, p)
	}
	for _, t := range templates {
		out.Templates = append(out.Templates, Template{
			ID: t.ID, GUID: deref(t.GUID), Name: t.Name, Filter: t.FilterName, DefaultExposure: t.DefaultExposure,
			Gain: t.Gain, Offset: t.Offset, Bin: t.Bin, TwilightLevel: twilightName(t.TwilightLevel),
			MoonEnabled: deref(t.MoonEnabled) != 0, MoonSeparation: t.MoonSeparation, MoonWidth: deref(t.MoonWidth),
			MoonDown: deref(t.MoonDownEnabled) != 0, MaximumHumidity: t.MaximumHumidity,
			UsedByPlans: usedPlans[t.ID], UsedByTargets: len(usedTargets[t.ID]),
		})
	}
	return out, nil
}

func commonSet(names []string) string {
	if len(names) == 0 {
		return "No targets"
	}
	first := names[0]
	for _, n := range names[1:] {
		if n != first {
			return "Mixed"
		}
	}
	return first
}

func panelNumber(name string) int {
	lower := strings.ToLower(name)
	i := strings.LastIndex(lower, "panel")
	if i < 0 {
		return 0
	}
	n := 0
	for _, c := range strings.TrimSpace(lower[i+5:]) {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func buildTarget(tr targetRow, plans []planRow, tmplBy map[int]templateRow, meas map[goals.Key]app.GoalMeasurement, in Inputs) Target {
	t := Target{ID: tr.ID, GUID: deref(tr.GUID), Name: tr.Name, Active: tr.Active != 0, RAHours: tr.RA, Dec: tr.Dec, Rotation: tr.Rotation, Panel: panelNumber(tr.Name), Progress: 1, GoalMode: goals.KindSNR}
	byFilter := map[string]*FilterGoal{}
	var order []string
	var setNames []string
	for _, p := range plans {
		tm := tmplBy[deref(p.TemplateID)]
		exp := p.Exposure
		if exp <= 0 {
			exp = tm.DefaultExposure
		}
		enabled := p.Enabled == nil || *p.Enabled != 0
		t.Plans = append(t.Plans, Plan{
			ID: p.ID, GUID: deref(p.GUID), TemplateID: deref(p.TemplateID), Template: tm.Name, Filter: tm.FilterName,
			Exposure: exp, ExposureRaw: p.Exposure, Desired: p.Desired, Acquired: p.Acquired, Accepted: p.Accepted,
			Enabled: enabled, Gain: tm.Gain, MoonSeparation: tm.MoonSeparation, MoonWidth: deref(tm.MoonWidth),
		})
		if !enabled {
			continue
		}
		setNames = append(setNames, tm.Name)
		f := tm.FilterName
		g, ok := byFilter[f]
		if !ok {
			g = &FilterGoal{Filter: f, StackKey: frameheader.NormalizeFilter(f)}
			byFilter[f] = g
			order = append(order, f)
		}
		g.Accepted += p.Accepted
		g.Desired += p.Desired
		g.ExposureHr += float64(p.Accepted) * exp / 3600
	}
	t.SetName = SetName(setNames)
	minEff := math.Inf(1)
	for _, f := range order {
		g := byFilter[f]
		key := goals.Key{Object: tr.Name, Filter: g.StackKey}
		goal, set := in.Goals[key]
		if !set {
			goal = goals.DefaultGoal(g.StackKey)
		}
		goal.TargetGUID = t.GUID
		goal.Filter = f
		g.Goal = goal
		g.GoalSet = set
		if set {
			t.GoalMode = goal.Kind
		}
		prog := 0.0
		eff := 0.0
		if m, ok := meas[key]; ok {
			if m.Error != nil {
				g.Error = *m.Error
			} else {
				ev := goals.Evaluate(m, goal)
				g.Progress = &ev
				g.Measured = true
				prog = ev.Progress
				eff = ev.EffectiveHours
				if ev.Done {
					prog = math.Max(prog, 1)
				}
			}
		}
		minEff = math.Min(minEff, eff)
		if t.Weakest == nil || prog < t.Progress {
			t.Progress = math.Min(prog, t.Progress)
			t.Weakest = g
		}
		t.Goals = append(t.Goals, *g)
	}
	if len(order) == 0 {
		t.Progress = 0
		minEff = 0
	}
	if math.IsInf(minEff, 1) {
		minEff = 0
	}
	t.Progress = math.Min(t.Progress, 1)
	t.EffHours = minEff
	t.Novelty = Novelty(t.Progress, minEff)
	if t.Weakest != nil {
		w := *t.Weakest
		t.Weakest = &w
	}
	sort.SliceStable(t.Plans, func(i, j int) bool { return t.Plans[i].ID < t.Plans[j].ID })
	return t
}

func fromTicks(v int64) time.Time {
	if v > 600000000000000000 {
		return time.Unix((v-621355968000000000)/10000000, 0).UTC()
	}
	return time.Unix(v, 0).UTC()
}

func (s *Snapshot) Project(id int) (Project, bool) {
	i := slices.IndexFunc(s.Projects, func(p Project) bool { return p.ID == id })
	if i < 0 {
		return Project{}, false
	}
	return s.Projects[i], true
}
