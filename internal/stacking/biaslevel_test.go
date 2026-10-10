package stacking

import (
	"math"
	"path/filepath"
	"testing"
)

func TestBiasMedianADU(t *testing.T) {
	t.Parallel()
	data := make([]float32, 100)
	for i := range data {
		data[i] = float32(500+i%7) / math.MaxUint16
	}
	data[3] = 1
	file := filepath.Join(t.TempDir(), "bias.fit")
	if err := writeFITSFile(file, 10, 10, 1, data, nil); err != nil {
		t.Fatal(err)
	}
	got, err := biasMedianADU(file)
	if err != nil {
		t.Fatal(err)
	}
	if got != 503 {
		t.Errorf("median %v ADU, want 503", got)
	}
}
