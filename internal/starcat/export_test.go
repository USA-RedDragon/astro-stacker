package starcat

import (
	"context"
	"path"
	"sort"
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

// WriteGaiaMirror stores stars as a complete mirror, two pieces per
// partition so readers must merge them.
func WriteGaiaMirror(ctx context.Context, b Bucket, prefix string, stars []GaiaStar) error {
	parts := map[int64][]GaiaStar{}
	for _, s := range stars {
		p := Pixel(GaiaOrder, s.RA, s.Dec)
		parts[p] = append(parts[p], s)
	}
	keys := make([]int64, 0, len(parts))
	for p := range parts {
		keys = append(keys, p)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	m := &Manifest{Catalog: "gaia-dr3", Schema: gaiaSchema().name, Order: GaiaOrder}
	for _, p := range keys {
		rows := parts[p]
		half := len(rows) / 2
		for i, chunk := range [][]GaiaStar{rows[:half], rows[half:]} {
			if len(chunk) == 0 {
				continue
			}
			pc, data, err := gaiaPiece(prefix, p, string(rune('a'+i)), chunk)
			if err != nil {
				return err
			}
			if err := b.Put(ctx, pc.Key, data); err != nil {
				return err
			}
			m.Pieces = append(m.Pieces, pc)
			m.Rows += pc.Rows
		}
	}
	if err := verifyPieces(ctx, b, gaiaSchema(), GaiaOrder, m.Pieces); err != nil {
		return err
	}
	data, err := encodeManifest(m)
	if err != nil {
		return err
	}
	return b.Put(ctx, path.Join(prefix, manifestName), data)
}

func StoreGaiaFile(ctx context.Context, o MirrorOptions, name, md5 string) error {
	var n atomic.Int64
	_, err := o.gaiaFileOnce(ctx, gaiaFile{Name: name, MD5: md5}, &n)
	return err
}
