package surveys

import "context"

func (f *Fetcher) neowise(_ context.Context, _ Field, _ Target) Fetch {
	return Fetch{Survey: "NEOWISE", State: Failed}
}
