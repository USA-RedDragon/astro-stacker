package surveys

import (
	"context"
	"time"
)

// Max ages of the cached survey answers that a refresh run fetches again.
// NEOWISE has none: the mission ended in August 2024 and its final release
// adds nothing new. The Gaia DR3 mirror has none either: it is a fixed
// release, not a cache.
const (
	// ATLASMaxAge is the age of a forced photometry result past which a
	// refresh queues the star at ATLAS again. ATLAS images the whole dark
	// sky every night or two, so a week old result misses several epochs;
	// six days, less than the weekly refresh, makes every weekly refresh
	// ask ATLAS once and no more.
	ATLASMaxAge = 6 * 24 * time.Hour
	// TESSMaxAge is the age of the TESS job's status past which a refresh
	// asks the job to look for new sectors. A sector lasts 27 days and its
	// light curves reach MAST weeks later, so a look every two weeks finds
	// a new sector within about two weeks of its release while keeping the
	// job to one MAST lookup per star per fortnight.
	TESSMaxAge = 13 * 24 * time.Hour
	// ZTFReleaseMaxAge is the age of a field's ZTF pull past which a
	// refresh checks IRSA for a newer data release. ZTF publishes one
	// every two to three months; a weekly check costs one directory
	// listing per field and bounds the wait for a new release to a week.
	ZTFReleaseMaxAge = 6 * 24 * time.Hour
)

type freshKey struct{}

// Fresh marks ctx so that fetches under it fetch again cached answers older
// than their survey's max age. A refresh keeps the older answer in use until
// the new one is in.
func Fresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshKey{}, true)
}

func isFresh(ctx context.Context) bool {
	v, _ := ctx.Value(freshKey{}).(bool)
	return v
}

// expired says a cached answer made at is older than maxAge at now; an
// answer of unknown age has expired.
func expired(at, now time.Time, maxAge time.Duration) bool {
	return at.IsZero() || now.Sub(at) > maxAge
}
