package app

import (
	"slices"
	"time"

	"gorm.io/gorm"
)

// Run night kinds.
const (
	RunNightDip     = "dip-watch"
	RunNightEclipse = "eclipse"
)

// RunNight is one night's imaging of a dip window or an eclipse request,
// judged when the night's imaging of it ended: the verdict, every
// measured sub and the averages, as the live card showed them. A run is
// one star, night and kind over one span of subs, whichever project the
// subs were taken under: subs belong to a star's field, not a project.
type RunNight struct {
	ID          int    `gorm:"primaryKey;autoIncrement"`
	ProjectGUID string `gorm:"not null;index"`
	// Night is the evening's civil date in the site's time zone, YYYY-MM-DD.
	Night       string    `gorm:"not null;uniqueIndex:idx_run_night_run,priority:2;index"`
	Kind        string    `gorm:"not null;default:'';uniqueIndex:idx_run_night_run,priority:3"`
	SourceID    int       `gorm:"not null;index;uniqueIndex:idx_run_night_run,priority:1"`
	RequestID   int       `gorm:"not null;default:0"`
	ProjectName string    `gorm:"not null;default:''"`
	Object      string    `gorm:"not null;default:''"`
	Filter      string    `gorm:"not null;default:''"`
	First       time.Time `gorm:"uniqueIndex:idx_run_night_run,priority:4"`
	Last        time.Time
	// Subs are the night's subs of the run, Measured how many gave a point.
	Subs     int
	Measured int
	// State and Note are the verdict: DipSeen, DipNone or DipUnsure with
	// its words for a dip window, the live card's state for an eclipse.
	State string `gorm:"not null;default:''"`
	Note  string `gorm:"type:text;not null;default:''"`
	// Run is how many subs in a row the dip judge averages, lasting
	// RunMinutes, Smallest the
	// least dip it would show, Scatter the single subs' scatter and Depth
	// the dip seen from DipStart.
	Run        int
	RunMinutes float64
	Smallest   float64
	Scatter    float64
	Depth      float64
	DipStart   *time.Time
	// Comps are the nearby stars used; AgreeLow and AgreeHigh the fewest
	// and most that agreed in one sub.
	Comps     int
	AgreeLow  int
	AgreeHigh int
	DepthLow  float64
	DepthHigh float64
	// DarkStart and DarkEnd bound the night's dark and high enough time;
	// LowFrom is when the star dropped below the planner's height limit
	// while subs were still taken.
	DarkStart *time.Time
	DarkEnd   *time.Time
	LowFrom   *time.Time
	// Zero is the raw magnitude the points are measured from.
	Zero   float64
	Points []RunPoint `gorm:"serializer:json"`
	Bins   []RunBin   `gorm:"serializer:json"`
	// Card is the live card as JSON, shown on the Now page until the
	// next night's imaging starts.
	Card      string `gorm:"type:text;not null;default:''"`
	JudgedAt  time.Time
	UpdatedAt time.Time
}

// RunPoint is one measured sub, in magnitudes fainter than the night's
// normal level.
type RunPoint struct {
	FrameID int       `json:"frame_id"`
	Time    time.Time `json:"time"`
	Filter  string    `json:"filter"`
	Mag     float64   `json:"mag"`
	Err     float64   `json:"err"`
	Comp    float64   `json:"comp"`
	Spread  float64   `json:"spread"`
	Agree   int       `json:"agree"`
	Present int       `json:"present"`
}

// RunBin is an average of a run of subs: its middle, ends, weighted mean,
// its own error and how many subs it holds.
type RunBin struct {
	Time  time.Time `json:"time"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Mag   float64   `json:"mag"`
	Err   float64   `json:"err"`
	Subs  int       `json:"subs"`
}

// LiveRequestStates are the eclipse request states whose project the
// scheduler images or imaged.
func LiveRequestStates() []string {
	return []string{EclipseSent, EclipseDone}
}

// SameRun says n and o are one run's night: the same star, night and
// kind, under the same project or over overlapping subs.
func (n RunNight) SameRun(o RunNight) bool {
	if n.SourceID != o.SourceID || n.Night != o.Night || n.Kind != o.Kind {
		return false
	}
	return n.ProjectGUID == o.ProjectGUID || (!n.First.After(o.Last) && !o.First.After(n.Last))
}

// StaleRunGUIDs are the projects of withdrawn, failed or unsent requests
// that no sent or done request shares: a cancelled, moved or held request
// whose run another project took over, or that was never imaged.
func StaleRunGUIDs(db *gorm.DB) (map[string]bool, error) {
	out := map[string]bool{}
	if !db.Migrator().HasTable(&EclipseRequest{}) {
		return out, nil
	}
	var reqs []EclipseRequest
	if err := db.Select("project_guid", "state").Where("project_guid IS NOT NULL AND project_guid <> ''").Find(&reqs).Error; err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, r := range reqs {
		if slices.Contains(LiveRequestStates(), r.State) {
			live[r.ProjectGUID] = true
		}
	}
	for _, r := range reqs {
		if !live[r.ProjectGUID] {
			out[r.ProjectGUID] = true
		}
	}
	return out, nil
}

// MergeRunNights keeps one row of each run among rows, in their order:
// the one under a project not in stale, then the one with the most subs,
// then the last judged. It returns the kept rows and the IDs of the rest.
func MergeRunNights(rows []RunNight, stale map[string]bool) ([]RunNight, []int) {
	var kept []RunNight
	var dropped []int
	for _, r := range rows {
		i := -1
		for j := range kept {
			if kept[j].SameRun(r) {
				i = j
				break
			}
		}
		switch {
		case i < 0:
			kept = append(kept, r)
		case betterRun(r, kept[i], stale):
			dropped = append(dropped, kept[i].ID)
			kept[i] = r
		default:
			dropped = append(dropped, r.ID)
		}
	}
	return kept, dropped
}

func betterRun(a, b RunNight, stale map[string]bool) bool {
	if stale[a.ProjectGUID] != stale[b.ProjectGUID] {
		return !stale[a.ProjectGUID]
	}
	if a.Subs != b.Subs {
		return a.Subs > b.Subs
	}
	return a.JudgedAt.After(b.JudgedAt)
}
