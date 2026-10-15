package cmd

import (
	"context"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/minio/minio-go/v7"
)

// surveyVariables reads the survey catalogues of variable stars from their
// mirrors in the survey bucket, and from VizieR while a mirror cannot.
func surveyVariables(cfg *config.Config, s3 *minio.Client) *discoveries.SurveyVariables {
	if s3 == nil || cfg.Discover.SurveyBucket == "" {
		return discoveries.NewSurveyVariables(nil)
	}
	return discoveries.NewSurveyVariables(starcat.MinioBucket{Client: s3, Name: cfg.Discover.SurveyBucket})
}

// addVariableMirrors adds a catalog-mirror command for each survey
// catalogue of variable stars, mirrored from CDS when its ReadMe changes.
func addVariableMirrors(add func(use, short, prefix, base string, run func(context.Context, starcat.MirrorOptions) error)) {
	for _, c := range []struct {
		use, short string
		cat        *starcat.VarCatalog
	}{
		{"gaia-vari", "Mirror Gaia DR3's variability classification (I/358/vclassre)", starcat.GaiaVariability()},
		{"atlas-var", "Mirror the ATLAS catalog of variable stars (J/AJ/156/241)", starcat.ATLASVariables()},
		{"ztf-var", "Mirror the ZTF catalog of periodic variable stars (J/ApJS/249/18)", starcat.ZTFVariables()},
		{"asassn-var", "Mirror the ASAS-SN catalog of variable stars (II/366)", starcat.ASASSNVariables()},
	} {
		add(c.use, c.short, c.cat.Prefix, c.cat.BaseURL, c.cat.Mirror)
	}
}
