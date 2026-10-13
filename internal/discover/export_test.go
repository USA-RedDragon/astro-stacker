package discover

import (
	"context"

	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
)

func (s *Service) MatchSubject(ctx context.Context, subj Subject) ([]Link, string) {
	if subj.HasPos {
		s.planQuery(ctx, &subj)
	}
	links, reason := s.matchSubject(ctx, subj)
	return trimSuggestions(links), reason
}

func NamedSHO(name string) bool { return namedSHO(name) }

func BortleCriterion(r starfront.Requirements, v skybright.Value) (Criterion, bool) {
	return bortleCriterion(r, v)
}
