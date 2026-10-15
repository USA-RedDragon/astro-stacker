package surveys

import "context"

func (f *Fetcher) tess(_ context.Context, _ Field, _ Target) Fetch {
	return Fetch{Survey: "TESS", State: Failed}
}

func (f *Fetcher) neowise(_ context.Context, _ Field, _ Target) Fetch {
	return Fetch{Survey: "NEOWISE", State: Failed}
}
