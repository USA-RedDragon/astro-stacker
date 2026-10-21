package claimswatch

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
)

// TNS searches the pipeline's local copy of the TNS object list.
type TNS struct {
	Index  func() *tnsmirror.Index
	Radius float64
}

func (t *TNS) Where() string { return WhereTNS }

func (t *TNS) Off() string {
	if t.Index == nil {
		return "The app keeps no copy of the TNS list here: it needs the TNS bot key."
	}
	return ""
}

func (t *TNS) Check(_ context.Context, stars []Star, _ State) (Outcome, error) {
	ix := t.Index()
	if ix == nil || ix.Len() == 0 {
		return Outcome{}, fmt.Errorf("the TNS copy has not been read yet")
	}
	radius := t.Radius
	if radius <= 0 {
		radius = 10
	}
	out := newOutcome()
	for _, s := range stars {
		for _, h := range ix.Near(s.RA, s.Dec, radius) {
			out.Found[s.SourceID] = append(out.Found[s.SourceID], TNSMention(h))
		}
	}
	return out, nil
}

// TNSMention describes a TNS object near a star.
func TNSMention(h tnsmirror.Hit) Mention {
	parts := []string{}
	if h.Type != "" {
		parts = append(parts, "Classed as "+h.Type)
	}
	if g := h.Group(); g != "" {
		parts = append(parts, "reported by "+g)
	}
	parts = append(parts, fmt.Sprintf(`%.1f" from your star`, h.SepArcsec))
	m := Mention{Key: fmt.Sprintf("objid:%d", h.ObjID), Title: h.FullName(), Detail: strings.Join(parts, ", ") + ".",
		URL: "https://www.wis-tns.org/object/" + url.PathEscape(h.Name)}
	if !h.Discovered.IsZero() {
		m.Dated = day(h.Discovered)
	}
	return m
}
