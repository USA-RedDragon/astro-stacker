package catalog

import (
	"context"
	"math"
)

type Object struct {
	ID          string   `json:"id"`
	Designation string   `json:"designation"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Aliases     []string `json:"aliases"`
	RA          float64  `json:"ra"`
	Dec         float64  `json:"dec"`
	MajorArcmin float64  `json:"majorArcmin"`
	MinorArcmin *float64 `json:"minorArcmin"`
	PA          *float64 `json:"pa"`
	Source      string   `json:"source"`
	Members     []string `json:"members,omitempty"`

	Magnitude         *float64 `json:"magnitude,omitempty"`
	SurfaceBrightness *float64 `json:"surfaceBrightness,omitempty"`
	Brightness        string   `json:"brightness,omitempty"`
	BrightScore       *float64 `json:"brightScore,omitempty"`
	Lists             []string `json:"lists,omitempty"`
}

type Store interface {
	Cone(ctx context.Context, raDeg, decDeg, radiusDeg float64) ([]Object, error)
	Search(ctx context.Context, query string, limit int) ([]Object, error)
}

const (
	TypeGalaxy        = "galaxy"
	TypeGalaxyGroup   = "galaxy-group"
	TypeEmission      = "emission"
	TypeReflection    = "reflection"
	TypeNebula        = "nebula"
	TypeDark          = "dark"
	TypePN            = "pn"
	TypeSNR           = "snr"
	TypeOpenCluster   = "open-cluster"
	TypeGlobular      = "globular"
	TypeClusterNebula = "cluster-nebula"
	TypeStar          = "star"
	TypeOther         = "other"
)

func (o Object) Minor() float64 {
	if o.MinorArcmin == nil {
		return 0
	}
	return *o.MinorArcmin
}

func (o Object) PAOr(fallback float64) float64 {
	if o.PA == nil || math.IsNaN(*o.PA) {
		return fallback
	}
	return *o.PA
}
