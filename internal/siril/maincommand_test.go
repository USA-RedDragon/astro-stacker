package siril

import "testing"

func TestMainCommand(t *testing.T) {
	for script, want := range map[string]string{
		"set16bits\nsetext fit\ncd reg\nsetref seq_ 1\nregister seq_ -prefix=r_\n": "register",
		"set32bits\nload raw0000.xisf\nsave raw0000\ncalibrate_single raw0000.fit -bias=b\n": "calibrate_single",
		"set32bits\nseqplatesolve pan -nocache\nseqapplyreg pan\nstack r_pan rej none\n": "seqplatesolve",
		"set32bits\nload x\n": "other",
	} {
		if got := mainCommand(script); got != want {
			t.Errorf("%q: got %s, want %s", script, got, want)
		}
	}
}
