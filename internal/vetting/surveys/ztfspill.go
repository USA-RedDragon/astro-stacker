package surveys

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
)

// tileSpill keeps a light curve file's objects on disk, one file per
// tile, so a pull holds one tile in memory at a time and not the whole
// file's footprint, which reaches 150 million epochs near the Galactic
// plane.
type tileSpill struct {
	dir   string
	tiles map[int64]*spillFile
}

type spillFile struct {
	f *os.File
	w *bufio.Writer
	n int
}

func (s *tileSpill) add(p int64, o *ztfObject) error {
	sf, ok := s.tiles[p]
	if !ok {
		f, err := os.CreateTemp(s.dir, "ztf-tile-*")
		if err != nil {
			return err
		}
		_ = os.Remove(f.Name())
		sf = &spillFile{f: f, w: bufio.NewWriterSize(f, 64<<10)}
		s.tiles[p] = sf
	}
	sf.n++
	b := make([]byte, 0, 2*binary.MaxVarintLen64+9+7*len(o.T))
	b = binary.AppendVarint(b, o.ID)
	b = binary.LittleEndian.AppendUint32(b, math.Float32bits(o.RA))
	b = binary.LittleEndian.AppendUint32(b, math.Float32bits(o.Dec))
	b = append(b, o.Band)
	b = binary.AppendUvarint(b, uint64(len(o.T)))
	b, err := binary.Append(b, binary.LittleEndian, o.T)
	if err != nil {
		return err
	}
	if b, err = binary.Append(b, binary.LittleEndian, o.Mag); err != nil {
		return err
	}
	b = append(b, o.Err...)
	_, err = sf.w.Write(b)
	return err
}

func (s *tileSpill) keys() []int64 {
	out := make([]int64, 0, len(s.tiles))
	for p := range s.tiles {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

var errSpill = errors.New("damaged ZTF tile spill")

// read gives back tile p's objects in the order they were added and
// frees the tile's file.
func (s *tileSpill) read(p int64) ([]*ztfObject, error) {
	sf, ok := s.tiles[p]
	if !ok {
		return nil, nil
	}
	defer func() {
		sf.f.Close()
		delete(s.tiles, p)
	}()
	if err := sf.w.Flush(); err != nil {
		return nil, err
	}
	if _, err := sf.f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(sf.f, 64<<10)
	out := make([]*ztfObject, 0, sf.n)
	var head [9]byte
	for range sf.n {
		id, err := binary.ReadVarint(r)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errSpill, err)
		}
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return nil, fmt.Errorf("%w: %w", errSpill, err)
		}
		n, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errSpill, err)
		}
		if n > math.MaxInt32 {
			return nil, fmt.Errorf("%w: %d epochs", errSpill, n)
		}
		o := &ztfObject{ID: id, RA: math.Float32frombits(binary.LittleEndian.Uint32(head[0:4])),
			Dec: math.Float32frombits(binary.LittleEndian.Uint32(head[4:8])), Band: head[8],
			T: make([]uint32, n), Mag: make([]int16, n), Err: make([]uint8, n)}
		if err := binary.Read(r, binary.LittleEndian, o.T); err != nil {
			return nil, fmt.Errorf("%w: %w", errSpill, err)
		}
		if err := binary.Read(r, binary.LittleEndian, o.Mag); err != nil {
			return nil, fmt.Errorf("%w: %w", errSpill, err)
		}
		if _, err := io.ReadFull(r, o.Err); err != nil {
			return nil, fmt.Errorf("%w: %w", errSpill, err)
		}
		out = append(out, o)
	}
	return out, nil
}

func (s *tileSpill) close() {
	for p, sf := range s.tiles {
		sf.f.Close()
		delete(s.tiles, p)
	}
}
