package ninapath_test

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/ninapath"
)

func TestFolder(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"161P/Hartley-IRAS":   "161P-Hartley-IRAS",
		"C/2024 T5 (ATLAS)":   "C-2024 T5 (ATLAS)",
		"M 31":                "M 31",
		" NGC 7000 ":          "NGC 7000",
		`a:b*c?d"e<f>g|h\i/j`: "a_b_c_d_e_f_g_h-i-j",
		"tab\there":           "tab_here",
		"":                    "",
		"..":                  "__",
		" . ":                 "_",
		"a..b":                "a..b",
	}
	for in, want := range cases {
		if got := ninapath.Folder(in); got != want {
			t.Errorf("Folder(%q) = %q, want %q", in, got, want)
		}
	}
}
