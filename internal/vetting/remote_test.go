package vetting

import "testing"

func TestParseRemoteAnswers(t *testing.T) {
	t.Parallel()
	ps1 := "#INFO\tx\n\n_r\tRAJ2000\tDEJ2000\tgmag\trmag\timag\n-----\t---\t---\t---\t---\t---\n" +
		"0.230\t010.684694730\t+41.269063770\t 13.2210\t12.6090\t11.6230\n1.704\t010.684767210\t+41.268529320\t        \t       \t11.8549\n"
	objs, err := parsePS1([]byte(ps1))
	if err != nil || len(objs) != 2 || objs[0].R != 12.609 || objs[0].SepArcsec != 0.23 || objs[1].G != 0 || objs[1].I != 11.8549 {
		t.Errorf("ps1 %+v %v", objs, err)
	}
	if objs, err := parsePS1([]byte("#INFO\tnothing\n")); err != nil || objs != nil {
		t.Errorf("empty ps1 %+v %v", objs, err)
	}
	det := `[{"mjd": 58466.28, "fid": 1, "magpsf": 14.17, "sigmapsf": 0.058, "isdiffpos": -1}, {"mjd": 58486.15, "fid": 2, "magpsf": 16.7, "sigmapsf": 0.08, "isdiffpos": 1}]`
	ds, err := parseAlerceDetections([]byte(det))
	if err != nil || len(ds) != 2 || ds[0].Band != "g" || ds[0].Positive || ds[1].Band != "r" || !ds[1].Positive || ds[1].Mag != 16.7 {
		t.Errorf("alerce %+v %v", ds, err)
	}
	head := "ebpminrp_gspphot,ebpminrp_gspphot_upper,teff_gspphot\n"
	if r, err := parseReddening([]byte(head + "0.2019,0.2113,5637.239\n")); err != nil || !r.Found || r.EBPRP != 0.2019 || r.EBPRPUpper != 0.2113 || r.Teff != 5637.239 {
		t.Errorf("gaia reddening %+v %v", r, err)
	}
	if r, err := parseReddening([]byte(head)); err != nil || r.Found {
		t.Errorf("gaia without parameters %+v %v", r, err)
	}
	if r, err := parseReddening([]byte(head + ",,\n")); err != nil || r.Found {
		t.Errorf("gaia with empty parameters %+v %v", r, err)
	}
	if _, err := parseReddening([]byte("<VOTABLE>")); err == nil {
		t.Error("gaia error page read as an answer")
	}
}
