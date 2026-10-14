package coverage

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	BasisHeader      = "header"
	BasisFileName    = "file name"
	BasisFolderName  = "folder name"
	BasisHandEntered = "hand-entered"
)
const (
	SourceImported = "imported"
	SourceFrames   = "frames"
)

type Basis struct {
	Night    string `json:"night"`
	Exposure string `json:"exposure"`
	Gain     string `json:"gain"`
	Offset   string `json:"offset"`
	SetTemp  string `json:"set_temp,omitempty"`
	BinX     string `json:"bin_x"`
}
type ImportedSet struct {
	calmatch.Set
	Basis       Basis
	HeaderError string
}

type importedSpec struct {
	typ, key, night string
	gain            float64
	filter          string
}

func importedSpecs() []importedSpec {
	const dir = "offset240/masters/"
	bias := dir + "masterBias_BIN-1_6248x4176.xisf"
	return []importedSpec{
		{biasType, bias, "2025-01-19", 0, ""},
		{biasType, bias, "2025-01-19", 100, ""},
		{darkType, dir + "masterDark_BIN-1_6248x4176_-20.00-EXPOSURE-300.00s.xisf", "2025-01-21", 0, ""},
		{darkType, dir + "masterDark_BIN-1_6248x4176_-20.00-EXPOSURE-600.00s.xisf", "2025-01-21", 100, ""},
		{flatType, dustBDir + "masterFlat_BIN-1_6248x4176_FILTER-Red_mono.xisf", dustBNight, 0, "Red"},
		{flatType, dustBDir + "masterFlat_BIN-1_6248x4176_FILTER-Green_mono.xisf", dustBNight, 0, "Green"},
		{flatType, dustBDir + "masterFlat_BIN-1_6248x4176_FILTER-Blue_mono.xisf", dustBNight, 0, "Blue"},
		{flatType, dustBDir + "masterFlat_BIN-1_6248x4176_FILTER-H-a_mono.xisf", dustBNight, 100, "H-a"},
	}
}

// The dust epoch from 2026-04-16 to 2026-05-27 had one dust shadow (disc
// B) and no flats. Its flats are made from each filter's 2025-05-13 master
// flat times disc B's shadow, fitted as a radial profile to the 2026-09-28
// flats at 142°, where it overlaps two later shadows. On that epoch's
// lights they left less in the dust than either real flat (the 2025 flat
// leaves disc B, the 2026 flat adds bright copies of the later shadows).
const (
	flatType   = "FLAT"
	dustBDir   = "dustfix/offset50/"
	dustBNight = "2026-04-30"
)

const (
	folderOffset = `(?:^|/)offset(\d+(?:\.\d+)?)/`
	fileBin      = `_BIN-(\d+)_`
	fileSetTemp  = `_(-?\d+(?:\.\d+)?)-EXPOSURE-`
	fileExposure = `EXPOSURE-(\d+(?:\.\d+)?)s`
)

func fromName(pattern, s string) (float64, bool) {
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if m == nil {
		return math.NaN(), false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	return v, err == nil
}
func fromKey(sp importedSpec) ImportedSet {
	night, _ := time.Parse("2006-01-02", sp.night)
	s := ImportedSet{
		Set: calmatch.Set{Type: sp.typ, Night: night, Gain: sp.gain, Filter: sp.filter, Exposure: 0, Offset: math.NaN(),
			SetTemp: math.NaN(), BinX: math.NaN(), Rotator: math.NaN(), Master: sp.key},
		Basis: Basis{Night: BasisHandEntered, Gain: BasisHandEntered, Exposure: BasisHandEntered},
	}
	if v, ok := fromName(folderOffset, sp.key); ok {
		s.Offset, s.Basis.Offset = v, BasisFolderName
	}
	if v, ok := fromName(fileBin, sp.key); ok {
		s.BinX, s.Basis.BinX = v, BasisFileName
	}
	if sp.typ == darkType {
		if v, ok := fromName(fileSetTemp, sp.key); ok {
			s.SetTemp, s.Basis.SetTemp = v, BasisFileName
		}
		if v, ok := fromName(fileExposure, sp.key); ok {
			s.Exposure, s.Basis.Exposure = v, BasisFileName
		}
	}
	return s
}
func Imported() []calmatch.Set {
	specs := importedSpecs()
	out := make([]calmatch.Set, 0, len(specs))
	for _, sp := range specs {
		out = append(out, fromKey(sp).Set)
	}
	return out
}
func ImportedSets(ctx context.Context, db *gorm.DB) ([]ImportedSet, error) {
	specs := importedSpecs()
	keys := make([]string, 0, len(specs))
	for _, sp := range specs {
		keys = append(keys, sp.key)
	}
	var frames []app.Frame
	if err := db.WithContext(ctx).Where("key IN ?", keys).Find(&frames).Error; err != nil {
		return nil, fmt.Errorf("load imported master headers: %w", err)
	}
	byKey := make(map[string]app.Frame, len(frames))
	for _, f := range frames {
		byKey[f.Key] = f
	}
	out := make([]ImportedSet, 0, len(specs))
	for _, sp := range specs {
		s := fromKey(sp)
		f, ok := byKey[sp.key]
		switch {
		case !ok:
			s.HeaderError = "master not indexed"
		case f.IndexError != nil:
			s.HeaderError = *f.IndexError
		default:
			overlayHeader(&s, f)
		}
		out = append(out, s)
	}
	return out, nil
}

func overlayHeader(s *ImportedSet, f app.Frame) {
	set := func(dst *float64, basis *string, v *float64) {
		if v != nil && !math.IsNaN(*v) {
			*dst, *basis = *v, BasisHeader
		}
	}
	set(&s.Offset, &s.Basis.Offset, f.Offset)
	set(&s.BinX, &s.Basis.BinX, f.BinX)
	if s.Type == darkType {
		set(&s.Exposure, &s.Basis.Exposure, f.Exposure)
		set(&s.Gain, &s.Basis.Gain, f.Gain)
		set(&s.SetTemp, &s.Basis.SetTemp, f.SetTemp)
	}
	if f.Night != nil {
		s.Night, s.Basis.Night = *f.Night, BasisHeader
	}
}
