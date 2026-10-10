package discover

import "context"

func (s *Service) MatchSubject(ctx context.Context, subj Subject) ([]Link, string) {
	links, reason := s.matchSubject(ctx, subj)
	return trimSuggestions(links), reason
}
