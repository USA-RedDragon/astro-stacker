package cmd

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
)

func TestCatalogMirrorHasTheVariableStarCatalogs(t *testing.T) {
	t.Parallel()
	c := newCatalogMirrorCommand()
	for use, prefix := range map[string]string{"gaia-vari": "catalogs/gaia-dr3-vari", "atlas-var": "catalogs/atlas-var", "ztf-var": "catalogs/ztf-var", "asassn-var": "catalogs/asassn-var"} {
		sc, _, err := c.Find([]string{use})
		if err != nil || sc.Name() != use {
			t.Fatalf("%s: %v", use, err)
		}
		if p := sc.Flags().Lookup("prefix").DefValue; p != prefix {
			t.Errorf("%s: prefix %s, want %s", use, p, prefix)
		}
		if u := sc.Flags().Lookup("base-url").DefValue; u == "" || u[len(u)-1] != '/' {
			t.Errorf("%s: base URL %q", use, u)
		}
	}
}

func TestSurveyVariablesWithoutS3ReadVizieR(t *testing.T) {
	t.Parallel()
	if surveyVariables(&config.Config{}, nil) == nil {
		t.Fatal("no survey catalogs without S3")
	}
}
