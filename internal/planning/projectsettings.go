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

func settingsOf(r projectRow) ProjectSettings {
	return ProjectSettings{
		DitherEvery:           deref(r.DitherEvery),
		FilterSwitchFrequency: deref(r.FilterSwitchFrequency),
		FlatsHandling:         deref(r.FlatsHandling),
		SmartExposureOrder:    deref(r.SmartExposureOrder) != 0,
		MeridianWindow:        deref(r.MeridianWindow),
		UseCustomHorizon:      deref(r.UseCustomHorizon) != 0,
		HorizonOffset:         deref(r.HorizonOffset),
		MaximumAltitude:       r.MaximumAltitude,
		EnableGrader:          r.EnableGrader != 0,
	}
}

func majority[T comparable](rows []projectRow, get func(projectRow) T) T {
	count, top := map[T]int{}, map[T]int{}
	for _, r := range rows {
		v := get(r)
		count[v]++
		if n, ok := top[v]; !ok || r.ID > n {
			top[v] = r.ID
		}
	}
	var best T
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
	out := ProjectSettingsDefaults{Basis: SettingsBasisTargetScheduler, Projects: len(own), ProfileID: profileID}
	if len(own) == 0 {
		out.ProjectSettings = TargetSchedulerProjectSettings()
		return out
	}
	out.Basis = SettingsBasisProfile
	out.DitherEvery = majority(own, func(r projectRow) int { return settingsOf(r).DitherEvery })
	out.FilterSwitchFrequency = majority(own, func(r projectRow) int { return settingsOf(r).FilterSwitchFrequency })
	out.FlatsHandling = majority(own, func(r projectRow) int { return settingsOf(r).FlatsHandling })
	out.SmartExposureOrder = majority(own, func(r projectRow) bool { return settingsOf(r).SmartExposureOrder })
	out.MeridianWindow = majority(own, func(r projectRow) int { return settingsOf(r).MeridianWindow })
	out.UseCustomHorizon = majority(own, func(r projectRow) bool { return settingsOf(r).UseCustomHorizon })
	out.HorizonOffset = majority(own, func(r projectRow) float64 { return settingsOf(r).HorizonOffset })
	out.MaximumAltitude = majority(own, func(r projectRow) float64 { return settingsOf(r).MaximumAltitude })
	out.EnableGrader = majority(own, func(r projectRow) bool { return settingsOf(r).EnableGrader })
	return out
}

func defaultProfile(projects []projectRow) string {
	return majority(projects, func(r projectRow) string { return r.ProfileID })
}

func (s *Snapshot) ProjectSettingsFor(profileID string) ProjectSettingsDefaults {
	return projectSettingsFor(s.projectRows, profileID)
}
