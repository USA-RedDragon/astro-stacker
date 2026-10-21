package cmd

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/binaries"
	"github.com/USA-RedDragon/astro-stacker/internal/claimswatch"
	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
	"gorm.io/gorm"
)

// adsEnv names the environment variable holding the NASA ADS API
// token, from a Kubernetes secret; without it ADS is shown as not checked.
const adsEnv = "ADS_API_TOKEN"

func claimsStars(svc *binaries.Service) func(ctx context.Context) ([]claimswatch.Star, error) {
	return func(ctx context.Context) ([]claimswatch.Star, error) {
		all, err := svc.Confirmed(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]claimswatch.Star, 0, len(all))
		for _, st := range all {
			name, gaia, tm, ra, dec := st.Identity()
			out = append(out, claimswatch.Star{SourceID: st.Source.ID, Name: name, GaiaID: gaia, TwoMASS: tm, RA: ra, Dec: dec})
		}
		return out, nil
	}
}

func claimsPlaces(svc *binaries.Service, tns *tnsmirror.Mirror) []claimswatch.Place {
	c := &http.Client{Timeout: 2 * time.Minute}
	var tnsIndex func() *tnsmirror.Index
	if tns != nil {
		tnsIndex = tns.Index
	}
	return []claimswatch.Place{
		&claimswatch.VSX{Client: c, Discoverer: func(ctx context.Context) string {
			d, err := svc.Discoverer(ctx)
			if err != nil {
				return ""
			}
			return d
		}},
		&claimswatch.TNS{Index: tnsIndex},
		&claimswatch.SIMBAD{Client: c},
		&claimswatch.ADS{Client: c, Token: func() string { return os.Getenv(adsEnv) }},
		&claimswatch.ArXiv{Client: c},
		&claimswatch.Alerts{Client: c, Since: earliestRecord()},
		&claimswatch.VizieR{Client: c},
	}
}

func earliestRecord() time.Time {
	var t time.Time
	for _, r := range claimswatch.Records() {
		if t.IsZero() || r.Published.Before(t) {
			t = r.Published
		}
	}
	return t
}

// startClaimsWatch runs the weekly claims watch, Sundays at 3 AM US
// Central, once the discovery pages are on.
func startClaimsWatch(ctx context.Context, db *gorm.DB, svc *binaries.Service, tns *tnsmirror.Mirror, task *activity.Task) *claimswatch.Service {
	if svc == nil {
		return nil
	}
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		slog.Error("Claims watch not started", "error", err)
		return nil
	}
	cw := &claimswatch.Service{DB: db, Stars: claimsStars(svc), Places: claimsPlaces(svc, tns), Task: task, Loc: loc,
		Weekday: time.Sunday, Hour: 3, FirstDelay: 10 * time.Minute}
	go cw.Run(ctx)
	slog.Info("Claims watch started", "ads", os.Getenv(adsEnv) != "", "tns", tns != nil)
	return cw
}
