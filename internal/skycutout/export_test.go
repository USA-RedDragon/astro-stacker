package skycutout

func (p Params) Step() float64 { return p.step() }

func (s *Service) Counted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recent)
}
