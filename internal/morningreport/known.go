package morningreport

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// KnownFind is a change of the night TNS already has, from the local TNS
// copy.
type KnownFind struct {
	Object string
	Text   string
	Early  bool
}

// known lists the night's changes the local TNS copy matched, once per
// target and TNS name.
func (c Collector) known(ctx context.Context, r *Report) {
	var rows []app.DiscoveryCandidate
	if err := c.App.WithContext(ctx).Select("object, matches").
		Where("night >= ? AND night < ? AND class = ? AND kind = ? AND vetting <> ? AND matches LIKE ?", r.Night, r.Night.AddDate(0, 0, 1),
			discoveries.ClassKnownTransient, app.DiscoveryKindChange, app.VetArtifact, `%"tns":%`).
		Order("object, snr DESC").Find(&rows).Error; err != nil {
		slog.Warn("Morning report: could not list known TNS objects", "error", err)
		return
	}
	at := map[string]int{}
	for _, row := range rows {
		var ms []discoveries.Match
		if json.Unmarshal([]byte(row.Matches), &ms) != nil {
			continue
		}
		m, ok := discoveries.TNSMatch(ms)
		if !ok {
			continue
		}
		key := row.Object + "\x00" + m.TNS.Name
		if i, seen := at[key]; seen {
			r.Known[i].Early = r.Known[i].Early || m.TNS.EarlyData
			continue
		}
		at[key] = len(r.Known)
		r.Known = append(r.Known, KnownFind{Object: row.Object, Text: discoveries.KnownText(*m.TNS), Early: m.TNS.EarlyData})
	}
}

// known is one line naming the night's changes TNS already has.
func (f Formatter) known(r Report) string {
	if len(r.Known) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Known))
	for _, k := range r.Known {
		p := esc(k.Object) + ", " + esc(k.Text)
		if k.Early {
			p += ". " + esc(discoveries.EarlyText)
		}
		parts = append(parts, p)
	}
	return "<b>In TNS</b> " + strings.Join(parts, "; ")
}
