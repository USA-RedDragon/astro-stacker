package discoverywatch

import (
	"context"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"gorm.io/gorm"
)

// Imaging states of a watch as the page shows them.
const (
	ImagingAsking       = "asking"
	ImagingStoppedQuiet = "stopped_quiet"
	ImagingStoppedUser  = "stopped_user"
)

// Night results from the user's own frames.
const (
	ResultSteady = "steady"
	ResultFaded  = "faded"
)

// NightView is one watched night: what was asked, the minutes got in its
// project and what the night's comparison found at the find.
type NightView struct {
	Night        string   `json:"night"`
	AskedMinutes int      `json:"asked_minutes"`
	Filter       string   `json:"filter"`
	GotMinutes   *float64 `json:"got_minutes"`
	Result       string   `json:"result"`
	State        string   `json:"state"`
	Note         string   `json:"note"`
}

// View is a find's watch as the page shows it.
type View struct {
	StartedAt        time.Time   `json:"started_at"`
	Confidence       string      `json:"confidence"`
	FadeNights       *int        `json:"fade_nights"`
	FadeNightsNeeded *int        `json:"fade_nights_needed"`
	Imaging          string      `json:"imaging"`
	ImagingStoppedAt *time.Time  `json:"imaging_stopped_at"`
	NightsPlanned    int         `json:"nights_planned"`
	NightsImaged     int         `json:"nights_imaged"`
	NextSurveyCheck  *string     `json:"next_survey_check"`
	Ended            string      `json:"ended"`
	Nights           []NightView `json:"nights"`
}

// endedWord is where a watch went when it ended: "look" for Needs a look
// and the other views, "artifact" for Likely artifacts.
func endedWord(w app.DiscoveryWatch) string {
	switch {
	case w.EndedAt == nil:
		return ""
	case w.EndReason == app.WatchEndArtifact:
		return "artifact"
	}
	return "look"
}

func imagingOf(w app.DiscoveryWatch) string {
	switch {
	case w.ImagingStoppedAt == nil:
		return ImagingAsking
	case w.StopReason == app.WatchStopUser:
		return ImagingStoppedUser
	}
	return ImagingStoppedQuiet
}

// Views are the watches of the sources in list that have one, by source.
func Views(ctx context.Context, db *gorm.DB, list []discoverysource.Summary) (map[int]*View, error) {
	out := map[int]*View{}
	if !db.Migrator().HasTable(&app.DiscoveryWatchNight{}) {
		return out, nil
	}
	db = db.WithContext(ctx)
	for _, s := range list {
		if s.Watch == nil {
			continue
		}
		v, err := viewOf(db, s)
		if err != nil {
			return nil, err
		}
		out[s.Source.ID] = v
	}
	return out, nil
}

func viewOf(db *gorm.DB, s discoverysource.Summary) (*View, error) {
	w := *s.Watch
	v := &View{StartedAt: w.StartedAt, FadeNights: w.Fades, FadeNightsNeeded: Needed(&w), Imaging: imagingOf(w),
		ImagingStoppedAt: w.ImagingStoppedAt, NightsPlanned: NightsAsked, Ended: endedWord(w), Nights: []NightView{}}
	if s.Vet != nil {
		v.Confidence = s.Vet.Confidence
		if s.Vet.FinishedAt != nil && w.EndedAt == nil {
			d := s.Vet.FinishedAt.Add(vetting.RefreshEvery).Format(time.DateOnly)
			v.NextSurveyCheck = &d
		}
	}
	var nights []app.DiscoveryWatchNight
	if err := db.Where("source_id = ?", w.SourceID).Order("night").Find(&nights).Error; err != nil {
		return nil, err
	}
	if len(nights) > 0 {
		v.NightsPlanned = len(nights)
	}
	faded := map[string]bool{}
	for _, m := range s.Members {
		faded[m.Night.Format(time.DateOnly)] = true
	}
	for _, n := range nights {
		day := n.Night.Format(time.DateOnly)
		nv := NightView{Night: day, AskedMinutes: n.Minutes, Filter: n.Filter, State: n.State, Note: n.Note}
		got, took, err := gotMinutes(db, n)
		if err != nil {
			return nil, err
		}
		if took {
			nv.GotMinutes = &got
		}
		if took && got > 0 {
			v.NightsImaged++
		}
		switch {
		case faded[day]:
			nv.Result = ResultFaded
		default:
			ok, err := compared(db, s.Source.Object, w.Group, n.Night)
			if err != nil {
				return nil, err
			}
			if ok {
				nv.Result = ResultSteady
			}
		}
		v.Nights = append(v.Nights, nv)
	}
	return v, nil
}

// gotMinutes is the exposure, in minutes, of the lights a night's project
// took; false until it took any.
func gotMinutes(db *gorm.DB, n app.DiscoveryWatchNight) (float64, bool, error) {
	if n.TargetGUID == "" {
		return 0, false, nil
	}
	var row struct {
		N   int
		Sum float64
	}
	if err := db.Model(&app.Frame{}).Select("COUNT(*) AS n, COALESCE(SUM(exposure), 0) AS sum").
		Where("type = ? AND ts_target = ?", "LIGHT", n.TargetGUID).Scan(&row).Error; err != nil {
		return 0, false, err
	}
	if row.N == 0 {
		return 0, false, nil
	}
	return row.Sum / 60, true, nil
}

// compared says a done run compared the field in the group on night.
func compared(db *gorm.DB, object, group string, night time.Time) (bool, error) {
	var n int64
	err := db.Model(&app.DiscoveryRun{}).Where("object = ? AND filter_group = ? AND state = ? AND night >= ? AND night < ?",
		object, group, app.DiscoveryDone, night, night.AddDate(0, 0, 1)).Count(&n).Error
	return n > 0, err
}
