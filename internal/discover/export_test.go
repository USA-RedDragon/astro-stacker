package discover

import "context"

func (s *Service) MatchSubject(ctx context.Context, subj Subject) ([]Link, string) {
	if subj.HasPos {
		s.planQuery(ctx, &subj)
	}
	links, reason := s.matchSubject(ctx, subj)
	return trimSuggestions(links), reason
}
