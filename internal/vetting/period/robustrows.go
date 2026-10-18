package period

import (
	"fmt"
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

func (a *analysis) periodWords() string {
	return fmtPeriod(a.eph.Period, a.eph.PeriodErr)
}

// partnerWords names the period that stands out instead, or "".
func partnerWords(r *Robust) string {
	if r.Partner <= 0 {
		return ""
	}
	if r.PartnerFrom == lc.TESS {
		return fmt.Sprintf(" TESS shows a %s-day cycle instead.", fmtPeriod(r.Partner, 0))
	}
	return fmt.Sprintf(" The cycle that stands out once the slow changes are taken out is %s days.", fmtPeriod(r.Partner, 0))
}

func (a *analysis) echoRow() check.Check {
	r := a.rob
	p := a.periodWords()
	if r.Echo != "" {
		near := fmt.Sprintf("%s days sits next to %s, where visits about once a night make false cycles", p, r.Echo)
		if r.Confirmed != "" {
			return check.New(check.Echo, check.Pass, fmt.Sprintf("%s, but %s show it too.", capitalize(near), r.Confirmed))
		}
		return check.New(check.Echo, check.NotRun, fmt.Sprintf("%s. ZTF alone cannot tell it from an echo; TESS or a night-long run of your own can.%s", capitalize(near), partnerWords(r)))
	}
	if a.kind == KindEclipsing {
		switch {
		case r.Few:
			return check.New(check.Echo, check.Fail, fmt.Sprintf("Points inside the dips come from %s in %s; one bad stretch of nights can make such dips.",
				plural(r.Nights, "night", "nights"), plural(r.Seasons, "season", "seasons")))
		case r.Band == "":
			return check.New(check.Echo, check.NotRun, "Too few ZTF or ATLAS points to test the dips once slow changes are taken out.")
		case !r.Stable:
			return check.New(check.Echo, check.Fail, fmt.Sprintf("Once changes slower than three weeks are taken out, the dips stand out at only %.0f sigma in %s.", r.BoxZ, r.Band))
		}
		return check.New(check.Echo, check.Pass, fmt.Sprintf("The dips stand out at %.0f sigma in %s once changes slower than three weeks are taken out, on %s over %s.",
			r.BoxZ, r.Band, plural(r.Nights, "night", "nights"), plural(r.Seasons, "season", "seasons")))
	}
	switch {
	case r.Band == "":
		return check.New(check.Echo, check.NotRun, "Too few ZTF or ATLAS points to test the cycle once slow changes are taken out.")
	case r.Unsteady:
		return check.New(check.Echo, check.Fail, fmt.Sprintf("The %s-day cycle comes at a different time in each season.%s", p, partnerWords(r)))
	case !r.Stable && a.eph.Period < shortPeriod:
		return check.New(check.Echo, check.Fail, fmt.Sprintf("The %s-day cycle goes away once changes slower than three weeks are taken out (%.1f sigma left in %s), so it echoes slow changes.%s",
			p, r.HPSNR, r.Band, partnerWords(r)))
	case !r.Stable:
		return check.New(check.Echo, check.Fail, fmt.Sprintf("The %s-day cycle does not stand out once each season's level is taken out.%s", p, partnerWords(r)))
	case a.eph.Period < shortPeriod:
		return check.New(check.Echo, check.Pass, fmt.Sprintf("The cycle still stands out at %.0f sigma in %s once changes slower than three weeks are taken out.", r.HPSNR, r.Band))
	}
	return check.New(check.Echo, check.Pass, fmt.Sprintf("The cycle stands out at %.0f sigma in %s once each season's level is taken out, at the same time every season.", r.HPSNR, r.Band))
}

func (a *analysis) comparisonRow() check.Check {
	r := a.rob
	if r.Comparisons == 0 {
		return check.New(check.Compare, check.NotRun, "No nearby star of the same brightness has enough ZTF points to compare.")
	}
	stars := plural(r.Comparisons, "nearby star", "nearby stars") + " of the same brightness"
	ratio := strconvRatio(r.CompRatio)
	if a.kind == KindEclipsing {
		if !r.CompPass {
			return check.New(check.Compare, check.Fail, fmt.Sprintf("The same search finds dips nearly as strong in %s: the star's stand out only %s times more, in %s.", stars, ratio, r.Band))
		}
		return check.New(check.Compare, check.Pass, fmt.Sprintf("The star's dips stand out %s times more than anything the same search finds in %s, in %s.", ratio, stars, r.Band))
	}
	switch {
	case r.CompSignal > compMaxSig:
		return check.New(check.Compare, check.Fail, fmt.Sprintf("%d of %s show the same %s-day cycle.", r.CompSignal, stars, a.periodWords()))
	case !r.CompPass:
		return check.New(check.Compare, check.Fail, fmt.Sprintf("The star's cycle is only %s times as large as in %s, in %s.", ratio, stars, r.Band))
	case r.CompSignal == 1:
		return check.New(check.Compare, check.Pass, fmt.Sprintf("1 of %s shows a trace of the cycle; the star's is %s times as large, in %s.", stars, ratio, r.Band))
	}
	return check.New(check.Compare, check.Pass, fmt.Sprintf("None of %s shows the cycle; the star's is %s times as large as theirs, in %s.", stars, ratio, r.Band))
}

func strconvRatio(v float64) string {
	if v >= 10 || math.IsInf(v, 0) {
		return fmt.Sprintf("%.0f", math.Min(v, 999))
	}
	return fmt.Sprintf("%.1f", v)
}
