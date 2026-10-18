package surveys

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestZTFImagesListsTheImagesOverASpot(t *testing.T) {
	t.Parallel()
	// IRSA's answer for 37624 on IC 1396 Panel 2, whose night of
	// 2025-08-03 ZTF imaged in the same hour to 20.5 and 20.8.
	body := readFixture(t, "ztfimages_37624.csv")
	var query string
	f, _ := testFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.String()
		_, _ = w.Write(body)
	}))
	imgs, err := f.ZTFImages(context.Background(), 324.145578, 56.393887, 2460889.5, 2460892.5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "/ibe/search/ztf/products/sci") || !strings.Contains(query, "obsjd+BETWEEN") {
		t.Errorf("query %s", query)
	}
	if len(imgs) != 4 {
		t.Fatalf("got %d images: %+v", len(imgs), imgs)
	}
	if imgs[0].Band != "r" || imgs[0].MagLimit != 20.782 || imgs[0].JD != 2460891.8261343 {
		t.Errorf("first image %+v", imgs[0])
	}
}

func TestZTFImagesLeavesOutFlaggedImages(t *testing.T) {
	t.Parallel()
	got, err := parseZTFImages([]byte("in_row_id,in_ra,in_dec,obsjd,filtercode,maglimit,infobits\n1,1,1,2460891.8,zr,20.7,0\n1,1,1,2460891.9,zg,20.1,33554432\n"))
	if err != nil || len(got) != 1 || got[0].Band != "r" {
		t.Errorf("got %+v, %v", got, err)
	}
	if got, err := parseZTFImages(nil); err != nil || got != nil {
		t.Errorf("empty answer: %+v, %v", got, err)
	}
}
