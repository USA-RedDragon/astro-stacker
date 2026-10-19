package app

import "time"

// DiscoveryWatch is a new find the vet calls real at low confidence, kept
// in Watching while more fades are looked for. Batch numbers the current
// set of nights asked for imaging; a new set is asked once every night of
// the last is over, until imaging stops.
type DiscoveryWatch struct {
	ID        int `gorm:"primaryKey;autoIncrement"`
	SourceID  int `gorm:"not null;uniqueIndex"`
	Batch     int `gorm:"not null;default:0"`
	StartedAt time.Time
	// Group is the filter group the find was detected in.
	Group string `gorm:"not null;default:''"`
	// FadesAtStart and Fades are the fade nights when the watch started and
	// now; nil until a vet report has counted them.
	FadesAtStart *int
	Fades        *int
	// StartNights is the dates of the fade nights at the start as a JSON
	// list, empty until a vet report has listed them; only nights not in
	// it count toward leaving.
	StartNights string `gorm:"type:text;not null;default:''"`
	// LastFadeAt is when the fade count last grew, or the start.
	LastFadeAt       time.Time
	ImagingStoppedAt *time.Time
	StopReason       string `gorm:"not null;default:''"`
	EndedAt          *time.Time
	EndReason        string `gorm:"not null;default:''"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Why a watch stopped asking for imaging.
const (
	WatchStopQuiet = "quiet"
	WatchStopUser  = "user"
)

// Why a watch ended.
const (
	WatchEndFades    = "fades"
	WatchEndRose     = "rose"
	WatchEndArtifact = "artifact"
	WatchEndUser     = "user"
	WatchEndOther    = "other"
)

// DiscoveryWatchNight is one night of imaging a watch asks Target
// Scheduler for: a project of its own with a normal event window over the
// night. States are those of an EclipseRequest.
type DiscoveryWatchNight struct {
	ID          int       `gorm:"primaryKey;autoIncrement"`
	SourceID    int       `gorm:"not null;index"`
	Batch       int       `gorm:"not null;default:0"`
	Night       time.Time `gorm:"type:date;not null"`
	Start       time.Time
	End         time.Time
	Minutes     int `gorm:"not null;default:0"`
	Filter      string
	Exposure    float64
	Subs        int
	ProjectGUID string `gorm:"index"`
	TargetGUID  string
	CreateCmd   string
	CloseCmd    string
	State       string `gorm:"not null;index"`
	Note        string `gorm:"type:text;not null;default:''"`
	// Settle is what is still owed the scheduler once the project is no
	// longer wanted, withdraw or close, empty once done.
	Settle    string `gorm:"not null;default:''"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
