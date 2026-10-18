package surveys

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// ZTFImage is one ZTF science image that covers a position: when it was
// taken, in which band, and the faintest magnitude it reached.
type ZTFImage struct {
	JD       float64 `json:"jd"`
	Band     string  `json:"band"`
	MagLimit float64 `json:"mag_limit"`
}

const ztfBandColumn = "filtercode"

// ztfImagesLimit bounds IRSA's answer: three days hold a few dozen images.
const ztfImagesLimit = 4 << 20

// ZTFImages lists the public ZTF science images IRSA holds over a
// position between two Julian dates, from the image metadata search,
// which needs no account. Images IRSA flags as bad are left out.
func (f *Fetcher) ZTFImages(ctx context.Context, ra, dec, jd0, jd1 float64) ([]ZTFImage, error) {
	q := url.Values{}
	q.Set("POS", fmt.Sprintf("%.6f,%.6f", ra, dec))
	q.Set("WHERE", fmt.Sprintf("obsjd BETWEEN %.5f AND %.5f", jd0, jd1))
	q.Set("COLUMNS", "obsjd,"+ztfBandColumn+",maglimit,infobits")
	q.Set("ct", "csv")
	b, err := f.getBytes(ctx, f.irsa()+"/ibe/search/ztf/products/sci?"+q.Encode(), ztfImagesLimit)
	if err != nil {
		return nil, fmt.Errorf("IRSA ZTF images: %w", err)
	}
	return parseZTFImages(b)
}

func parseZTFImages(b []byte) ([]ZTFImage, error) {
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("IRSA ZTF images: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	for _, need := range []string{"obsjd", ztfBandColumn, "maglimit", "infobits"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("IRSA ZTF images: no %s column", need)
		}
	}
	var out []ZTFImage
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("IRSA ZTF images: %w", err)
		}
		get := func(name string) string {
			if i := col[name]; i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		jd, err1 := strconv.ParseFloat(get("obsjd"), 64)
		lim, err2 := strconv.ParseFloat(get("maglimit"), 64)
		bits, err3 := strconv.ParseInt(get("infobits"), 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || bits != 0 {
			continue
		}
		out = append(out, ZTFImage{JD: jd, Band: strings.TrimPrefix(get(ztfBandColumn), "z"), MagLimit: lim})
	}
	return out, nil
}
