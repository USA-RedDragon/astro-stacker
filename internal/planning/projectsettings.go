package planning

const (
	SettingsBasisProfile         = "profile"
	SettingsBasisTargetScheduler = "target-scheduler"
)

type ProjectSettings struct {
	DitherEvery           int     `json:"ditherEvery"`
	FilterSwitchFrequency int     `json:"filterSwitchFrequency"`
	FlatsHandling         int     `json:"flatsHandling"`
	SmartExposureOrder    bool    `json:"smartExposureOrder"`
	MeridianWindow        int     `json:"meridianWindow"`
	UseCustomHorizon      bool    `json:"useCustomHorizon"`
	HorizonOffset         float64 `json:"horizonOffset"`
	MaximumAltitude       float64 `json:"maximumAltitude"`
	EnableGrader          bool    `json:"enableGrader"`
}

type ProjectSettingsDefaults struct {
	ProjectSettings
	Basis     string `json:"basis"`
	Projects  int    `json:"projects"`
	ProfileID string `json:"profileId"`
}

func TargetSchedulerProjectSettings() ProjectSettings {
	return ProjectSettings{EnableGrader: true}
}

func intFlag(v *int) *bool {
	if v == nil {
		return nil
	}
	b := *v != 0
	return &b
}

func majority[T comparable](rows []projectRow, get func(projectRow) *T, fallback T) T {
	count, top := map[T]int{}, map[T]int{}
	for _, r := range rows {
		p := get(r)
		if p == nil {
			continue
		}
		v := *p
		count[v]++
		if n, ok := top[v]; !ok || r.ID > n {
			top[v] = r.ID
		}
	}
	best := fallback
	bestN, bestID := 0, 0
	for v, n := range count {
		if n > bestN || (n == bestN && top[v] > bestID) {
			best, bestN, bestID = v, n, top[v]
		}
	}
	return best
}

func projectSettingsFor(projects []projectRow, profileID string) ProjectSettingsDefaults {
	var own []projectRow
	for _, p := range projects {
		if p.ProfileID == profileID {
			own = append(own, p)
		}
	}
	ts := TargetSchedulerProjectSettings()
	out := ProjectSettingsDefaults{Basis: SettingsBasisTargetScheduler, Projects: len(own), ProfileID: profileID, ProjectSettings: ts}
	if len(own) == 0 {
		return out
	}
	out.Basis = SettingsBasisProfile
	out.DitherEvery = majority(own, func(r projectRow) *int { return r.DitherEvery }, ts.DitherEvery)
	out.FilterSwitchFrequency = majority(own, func(r projectRow) *int { return r.FilterSwitchFrequency }, ts.FilterSwitchFrequency)
	out.FlatsHandling = majority(own, func(r projectRow) *int { return r.FlatsHandling }, ts.FlatsHandling)
	out.SmartExposureOrder = majority(own, func(r projectRow) *bool { return intFlag(r.SmartExposureOrder) }, ts.SmartExposureOrder)
	out.MeridianWindow = majority(own, func(r projectRow) *int { return r.MeridianWindow }, ts.MeridianWindow)
	out.UseCustomHorizon = majority(own, func(r projectRow) *bool { return intFlag(r.UseCustomHorizon) }, ts.UseCustomHorizon)
	out.HorizonOffset = majority(own, func(r projectRow) *float64 { return r.HorizonOffset }, ts.HorizonOffset)
	out.MaximumAltitude = majority(own, func(r projectRow) *float64 { return r.MaximumAltitude }, ts.MaximumAltitude)
	out.EnableGrader = majority(own, func(r projectRow) *bool { b := r.EnableGrader != 0; return &b }, ts.EnableGrader)
	return out
}

func defaultProfile(projects []projectRow) string {
	return majority(projects, func(r projectRow) *string { return &r.ProfileID }, "")
}

func (s *Snapshot) ProjectSettingsFor(profileID string) ProjectSettingsDefaults {
	return projectSettingsFor(s.projectRows, profileID)
}
