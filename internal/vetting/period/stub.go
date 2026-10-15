package period

import "github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"

func phaseOf(_ *Ephemeris, _ float64) float64 { return 0 }

func predict(_ *Ephemeris, _ float64, _ string) float64 { return 0 }

func analyze(_ []lc.Series, _ Options) (*Result, error) { return nil, ErrNoPeriod }

func timeMinimum(_ *Ephemeris, _ []lc.Point, _ string) (Timing, bool) { return Timing{}, false }
