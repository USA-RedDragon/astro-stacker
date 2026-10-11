package wake_test

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/wake"
)

func TestNotifyWakesEveryWaiter(t *testing.T) {
	t.Parallel()
	var s wake.Signal
	a, b := s.C(), s.C()
	s.Notify()
	for _, c := range []<-chan struct{}{a, b} {
		select {
		case <-c:
		case <-time.After(time.Second):
			t.Fatal("waiter not woken")
		}
	}
	select {
	case <-s.C():
		t.Fatal("a channel taken after Notify fired early")
	default:
	}
}

func TestNotifyBeforeWaitIsNotLost(t *testing.T) {
	t.Parallel()
	var s wake.Signal
	c := s.C()
	s.Notify()
	select {
	case <-c:
	default:
		t.Fatal("Notify between C and the wait was lost")
	}
}

func TestNilSignal(t *testing.T) {
	t.Parallel()
	var s *wake.Signal
	s.Notify()
	if s.C() != nil {
		t.Fatal("nil Signal returned a channel")
	}
}
