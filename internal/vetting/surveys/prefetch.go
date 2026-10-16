package surveys

import (
	"context"
	"strings"
)

// prefetch pulls the field's ZTF light curve files. NEOWISE is read per
// star instead: a footprint holds 9 to 22 million single exposure rows,
// more than IRSA's TAP service returns in one query.
func (f *Fetcher) prefetch(ctx context.Context, field Field) error {
	f.pullMu.Lock()
	defer f.pullMu.Unlock()
	return f.prefetchZTF(ctx, field)
}

// Sizes is the bytes cached per field slug and survey.
func (f *Fetcher) Sizes(ctx context.Context) (map[string]map[string]int64, error) {
	top, err := f.Store.Sizes(ctx, f.prefix())
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int64{}
	for slug := range top {
		if slug == "" || strings.Contains(slug, ".") {
			continue
		}
		s, err := f.Store.Sizes(ctx, f.prefix()+slug+"/")
		if err != nil {
			return nil, err
		}
		out[slug] = s
	}
	return out, nil
}
