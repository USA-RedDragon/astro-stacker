package starcat

import (
	"context"
	"sync/atomic"
)

func ParseMD5Sums(b []byte) (int, error) {
	f, err := parseMD5Sums(b)
	return len(f), err
}

func RoundTripGaia(stars []GaiaStar) ([]GaiaStar, error) {
	b, err := encodePiece(gaiaSchema(), stars)
	if err != nil {
		return nil, err
	}
	var out []GaiaStar
	_, err = decodePiece(gaiaSchema(), b, [3]float64{}, -1, func(r []GaiaStar) { out = append(out, r...) })
	return out, err
}

func RoundTripVSX(stars []VSXStar) ([]VSXStar, error) {
	b, err := encodePiece(vsxSchema(), stars)
	if err != nil {
		return nil, err
	}
	var out []VSXStar
	_, err = decodePiece(vsxSchema(), b, [3]float64{}, -1, func(r []VSXStar) { out = append(out, r...) })
	return out, err
}

func ParseVSXLine(line string) (VSXStar, error) { return vsxRow([]byte(line)) }

func StoreGaiaFile(ctx context.Context, o MirrorOptions, name, md5 string) error {
	var n atomic.Int64
	_, err := o.gaiaFileOnce(ctx, gaiaFile{Name: name, MD5: md5}, &n)
	return err
}
