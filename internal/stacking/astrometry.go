package stacking

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/USA-RedDragon/astro-stacker/internal/astrometry"
)

// prepareAstrometry puts astrometry.net's index files in place, checked
// against their checksums, and readies the solver; on failure masters are
// solved with Siril alone until the next start.
func (p *Pipeline) prepareAstrometry(ctx context.Context) {
	s := p.opts.Astrometry
	if s == nil {
		return
	}
	if err := astrometry.EnsureIndex(ctx, &http.Client{}, s.IndexDir); err != nil {
		slog.Error("Could not prepare astrometry.net index files; plate solving with Siril alone", "dir", s.IndexDir, "error", err)
		return
	}
	s.MarkReady()
	slog.Info("astrometry.net ready", "index", s.IndexDir, "set", astrometry.IndexSetID())
}
