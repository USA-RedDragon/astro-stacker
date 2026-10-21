package morningreport

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/claimswatch"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// claims reads the mentions the claims watch first found in the window.
func (c Collector) claims(ctx context.Context, r *Report) {
	db := c.App.WithContext(ctx)
	if !db.Migrator().HasTable(&app.ClaimMention{}) {
		return
	}
	svc := claimswatch.Service{DB: c.App}
	found, err := svc.FoundBetween(ctx, r.Start, r.End)
	if err != nil {
		slog.Warn("Morning report: could not read the claims watch", "error", err)
		return
	}
	r.Claims = found
	if len(found) == 0 {
		return
	}
	var n int64
	if err := db.Model(&app.ClaimCheck{}).Distinct("source_id").Count(&n).Error; err != nil {
		slog.Warn("Morning report: could not count the watched stars", "error", err)
	}
	r.ClaimStars = int(n)
}

// claims is the Your discoveries lines, when a check found a new mention.
func (f Formatter) claims(r Report) string {
	if len(r.Claims) == 0 {
		return ""
	}
	lines := []string{"<b>Your discoveries</b>"}
	stars := map[int]bool{}
	for _, m := range r.Claims {
		stars[m.SourceID] = true
		name := fmt.Sprintf("%d", m.SourceID)
		if m.StarName != "" {
			name += " (" + esc(m.StarName) + ")"
		}
		line := "New mention: " + name + " is in " + esc(m.Label) + ": " + esc(m.Title)
		if m.Dated != nil {
			line += ", dated " + f.date(*m.Dated)
		} else {
			line += ", first found " + f.date(m.FirstSeen)
		}
		line += "."
		if m.Record != nil {
			line += " Your " + esc(m.Record.Name) + " record is dated " + f.date(m.Record.Published) + "."
		}
		if m.URL != "" {
			line += ` <a href="` + esc(m.URL) + `">Open</a>`
		}
		lines = append(lines, line)
	}
	if rest := r.ClaimStars - len(stars); rest > 0 {
		lines = append(lines, "The other "+plural(rest, "star has", "stars have")+" no new mentions.")
	}
	return strings.Join(lines, "\n")
}

func (f Formatter) date(t time.Time) string {
	loc := f.Loc
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("Jan 2")
}
