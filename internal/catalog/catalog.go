package catalog

import "context"

type Object struct {
	ID          string   `json:"id"`
	Designation string   `json:"designation"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Aliases     []string `json:"aliases"`
	RA          float64  `json:"ra"`
	Dec         float64  `json:"dec"`
	MajorArcmin float64  `json:"majorArcmin"`
	MinorArcmin float64  `json:"minorArcmin"`
	PA          float64  `json:"pa"`
	Source      string   `json:"source"`
}

type Store interface {
	Cone(ctx context.Context, raDeg, decDeg, radiusDeg float64) ([]Object, error)
	Search(ctx context.Context, query string, limit int) ([]Object, error)
}
