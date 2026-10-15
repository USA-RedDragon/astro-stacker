package surveys

import "context"

func (f *Fetcher) ztf(_ context.Context, _ Field, _ Target) Fetch {
	return Fetch{Survey: "ZTF", State: Failed}
}

func (f *Fetcher) atlas(_ context.Context, _ Field, _ Target) Fetch {
	return Fetch{Survey: "ATLAS", State: Failed}
}

func (f *Fetcher) tess(_ context.Context, _ Field, _ Target) Fetch {
	return Fetch{Survey: "TESS", State: Failed}
}

func (f *Fetcher) neowise(_ context.Context, _ Field, _ Target) Fetch {
	return Fetch{Survey: "NEOWISE", State: Failed}
}

func (f *Fetcher) prefetch(_ context.Context, _ Field) error { return nil }
