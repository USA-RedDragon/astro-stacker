package planning

type projectRow struct {
	ID              int     `gorm:"column:Id;primaryKey"`
	ProfileID       string  `gorm:"column:profileId"`
	Name            string  `gorm:"column:name"`
	Description     *string `gorm:"column:description"`
	State           int     `gorm:"column:state"`
	Priority        int     `gorm:"column:priority"`
	MinimumTime     int     `gorm:"column:minimumtime"`
	MinimumAltitude float64 `gorm:"column:minimumaltitude"`
	MaximumAltitude float64 `gorm:"column:maximumAltitude"`
	IsMosaic        int     `gorm:"column:isMosaic"`
	EnableGrader    int     `gorm:"column:enablegrader"`
	GUID            *string `gorm:"column:guid"`
}

func (projectRow) TableName() string { return "project" }

type targetRow struct {
	ID        int      `gorm:"column:Id;primaryKey"`
	Name      string   `gorm:"column:name"`
	Active    int      `gorm:"column:active"`
	RA        *float64 `gorm:"column:ra"`
	Dec       *float64 `gorm:"column:dec"`
	Rotation  float64  `gorm:"column:rotation"`
	ProjectID *int     `gorm:"column:projectid"`
	GUID      *string  `gorm:"column:guid"`
}

func (targetRow) TableName() string { return "target" }

type planRow struct {
	ID         int     `gorm:"column:Id;primaryKey"`
	ProfileID  string  `gorm:"column:profileId"`
	Exposure   float64 `gorm:"column:exposure"`
	Desired    int     `gorm:"column:desired"`
	Acquired   int     `gorm:"column:acquired"`
	Accepted   int     `gorm:"column:accepted"`
	TargetID   *int    `gorm:"column:targetid"`
	TemplateID *int    `gorm:"column:exposureTemplateId"`
	Enabled    *int    `gorm:"column:enabled"`
	GUID       *string `gorm:"column:guid"`
}

func (planRow) TableName() string { return "exposureplan" }

type templateRow struct {
	ID              int     `gorm:"column:Id;primaryKey"`
	ProfileID       string  `gorm:"column:profileId"`
	Name            string  `gorm:"column:name"`
	FilterName      string  `gorm:"column:filtername"`
	Gain            *int    `gorm:"column:gain"`
	Offset          *int    `gorm:"column:offset"`
	Bin             *int    `gorm:"column:bin"`
	TwilightLevel   *int    `gorm:"column:twilightlevel"`
	MoonEnabled     *int    `gorm:"column:moonavoidanceenabled"`
	MoonSeparation  float64 `gorm:"column:moonavoidanceseparation"`
	MoonWidth       *int    `gorm:"column:moonavoidancewidth"`
	MaximumHumidity float64 `gorm:"column:maximumhumidity"`
	DefaultExposure float64 `gorm:"column:defaultexposure"`
	MoonRelaxScale  float64 `gorm:"column:moonrelaxscale"`
	MoonDownEnabled *int    `gorm:"column:moondownenabled"`
	DitherEvery     *int    `gorm:"column:ditherevery"`
	GUID            *string `gorm:"column:guid"`
}

func (templateRow) TableName() string { return "exposuretemplate" }

type ruleWeightRow struct {
	ID        int     `gorm:"column:Id;primaryKey"`
	Name      string  `gorm:"column:name"`
	Weight    float64 `gorm:"column:weight"`
	ProjectID *int    `gorm:"column:projectid"`
}

func (ruleWeightRow) TableName() string { return "ruleweight" }

type seasonRow struct {
	TargetGUID  string  `gorm:"column:target_guid;primaryKey"`
	NightsLeft  int     `gorm:"column:nights_left"`
	OutOfSeason int     `gorm:"column:out_of_season"`
	SeasonEnd   *string `gorm:"column:season_end"`
	ComputedFor *string `gorm:"column:computed_for"`
}

func (seasonRow) TableName() string { return "ts_target_season" }
