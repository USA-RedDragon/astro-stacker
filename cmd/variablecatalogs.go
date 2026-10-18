package cmd

import (
	"context"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/minio/minio-go/v7"
)

// surveyVariables reads the survey catalogues of variable stars from their
// mirrors in the survey bucket, and from VizieR while a mirror cannot or
// the mirrors are off.
func surveyVariables(cfg *config.Config, s3 *minio.Client) *discoveries.SurveyVariables {
	if s3 == nil || !cfg.Discover.CatalogMirror || cfg.Discover.SurveyBucket == "" {
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
		{"tess-eb-new", "Mirror the TESS Ten Thousand Catalog's new eclipsing binaries (J/ApJS/279/50 table3)", starcat.TESSEBNew()},
		{"tess-eb-known", "Mirror the TESS Ten Thousand Catalog's known eclipsing binaries (J/ApJS/279/50 table4)", starcat.TESSEBKnown()},
		{"tess-eb-prsa", "Mirror the TESS eclipsing binaries of sectors 1 to 26 (J/ApJS/258/16)", starcat.TESSEBPrsa()},
		{"ztf-sus", "Mirror the ZTF catalog of suspected variable stars (J/ApJS/249/18 table3)", starcat.ZTFSuspected()},
		{"tess-rot", "Mirror the TESS rotation periods of young stars (J/AJ/164/137)", starcat.TESSRotation()},
		{"gcvs-evs", "Mirror the GCVS catalog of variable stars in other galaxies (B/gcvs/evs_cat)", starcat.GCVSExtragalactic()},
		{"maiz-var", "Mirror the Gaia DR3 photometric dispersions of Maiz Apellaniz et al. 2023 (J/A+A/677/A137)", starcat.MaizVariability()},
	} {
		add(c.use, c.short, c.cat.Prefix, c.cat.BaseURL, c.cat.Mirror)
	}
}
