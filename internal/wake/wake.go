// Package wake lets one loop wake others that sleep between polls.
package wake

import "sync"

// Signal wakes every loop waiting on it. A loop takes C before it looks
// for work and waits on that channel after finding none, so a Notify that
// comes while it looks is not lost. A nil Signal never fires.
type Signal struct {
	mu sync.Mutex
	ch chan struct{}
}

// C returns a channel that is closed at the next Notify.
func (s *Signal) C() <-chan struct{} {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	return s.ch
}

// Notify wakes every loop waiting on a channel from C.
func (s *Signal) Notify() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch != nil {
		close(s.ch)
	}
	s.ch = make(chan struct{})
}
