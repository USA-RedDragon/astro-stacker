package ownimage

import (
	"encoding/binary"
	"log/slog"
	"os"
	"sync"
)

// stamps keeps the stamps cut around each spot from its night and
// reference subs, on disk when it has a file: a target with 700 spots and
// 1,200 subs cuts 3 GB of them.
type stamps struct {
	mu  sync.Mutex
	f   *os.File
	end int64
}

// stampRef finds one kept stamp.
type stampRef struct {
	mem []float32
	off int64
	ok  bool
}

const stampBytes = stampSide * stampSide * 4

func newStamps(dir string) (*stamps, error) {
	if dir == "" {
		return &stamps{}, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "stamps-*")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(f.Name())
	return &stamps{f: f}, nil
}

func (s *stamps) put(v []float32) (stampRef, error) {
	if s.f == nil {
		return stampRef{mem: v, ok: true}, nil
	}
	b, err := binary.Append(make([]byte, 0, stampBytes), binary.LittleEndian, v)
	if err != nil {
		return stampRef{}, err
	}
	s.mu.Lock()
	off := s.end
	s.end += int64(len(b))
	s.mu.Unlock()
	if _, err := s.f.WriteAt(b, off); err != nil {
		return stampRef{}, err
	}
	return stampRef{off: off, ok: true}, nil
}

func (s *stamps) get(r stampRef) []float32 {
	if !r.ok {
		return nil
	}
	if r.mem != nil {
		return r.mem
	}
	b := make([]byte, stampBytes)
	if _, err := s.f.ReadAt(b, r.off); err != nil {
		slog.Warn("Could not read back a vetting stamp", "error", err)
		return nil
	}
	v := make([]float32, stampSide*stampSide)
	if _, err := binary.Decode(b, binary.LittleEndian, v); err != nil {
		return nil
	}
	return v
}

func (s *stamps) close() {
	if s.f != nil {
		_ = s.f.Close()
	}
}
