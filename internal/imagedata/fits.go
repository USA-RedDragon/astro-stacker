package imagedata

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	fitsBlock = 2880
	fitsCard  = 80
)

// decodeFITS decodes the primary HDU of an uncompressed FITS file with 2 or 3
// axes (the third being channels).
func decodeFITS(b []byte) (*Image, error) {
	kw := map[string]string{}
	end := -1
	for off := 0; off+fitsCard <= len(b); off += fitsCard {
		card := string(b[off : off+fitsCard])
		key := strings.TrimSpace(card[:8])
		if key == "END" {
			end = off + fitsCard
			break
		}
		if card[8:10] == "= " {
			v := card[10:]
			if i := strings.Index(v, "/"); i >= 0 && !strings.HasPrefix(strings.TrimSpace(v), "'") {
				v = v[:i]
			}
			kw[key] = strings.Trim(strings.TrimSpace(v), "'")
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("%w: FITS header has no END", ErrUnsupported)
	}
	dataStart := (end + fitsBlock - 1) / fitsBlock * fitsBlock

	atoi := func(k string) int {
		v, _ := strconv.Atoi(strings.TrimSpace(kw[k]))
		return v
	}
	atof := func(k string, def float64) float64 {
		v, err := strconv.ParseFloat(strings.TrimSpace(kw[k]), 64)
		if err != nil {
			return def
		}
		return v
	}
	bitpix := atoi("BITPIX")
	naxis := atoi("NAXIS")
	if naxis < 2 || naxis > 3 {
		return nil, fmt.Errorf("%w: NAXIS=%d", ErrUnsupported, naxis)
	}
	w, h, c := atoi("NAXIS1"), atoi("NAXIS2"), 1
	if naxis == 3 {
		c = atoi("NAXIS3")
	}
	bzero, bscale := atof("BZERO", 0), atof("BSCALE", 1)
	size := int(math.Abs(float64(bitpix))) / 8
	count := w * h * c
	if size == 0 || dataStart+count*size > len(b) {
		return nil, fmt.Errorf("%w: FITS data truncated or BITPIX=%d", ErrUnsupported, bitpix)
	}
	raw := b[dataStart:]
	be := binary.BigEndian
	im := &Image{W: w, H: h, C: c, Data: make([]float32, count)}

	// Integer data is normalized to its type's range after BZERO/BSCALE,
	// which maps the usual unsigned 16-bit (BZERO=32768) to [0, 1].
	switch bitpix {
	case 8:
		for i := range count {
			im.Data[i] = float32((float64(raw[i])*bscale + bzero) / math.MaxUint8)
		}
	case 16:
		for i := range count {
			v := float64(int16(be.Uint16(raw[2*i:])))*bscale + bzero
			im.Data[i] = float32(v / math.MaxUint16)
		}
	case 32:
		for i := range count {
			v := float64(int32(be.Uint32(raw[4*i:])))*bscale + bzero
			im.Data[i] = float32(v / math.MaxUint32)
		}
	case -32:
		for i := range count {
			im.Data[i] = float32(float64(math.Float32frombits(be.Uint32(raw[4*i:])))*bscale + bzero)
		}
	case -64:
		for i := range count {
			im.Data[i] = float32(math.Float64frombits(be.Uint64(raw[8*i:]))*bscale + bzero)
		}
	default:
		return nil, fmt.Errorf("%w: BITPIX=%d", ErrUnsupported, bitpix)
	}
	return im, nil
}
