package observatory_test

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
)

func TestHorizonProviderOnPostgres(t *testing.T) {
	t.Parallel()
	db := testdb.Postgres(t, "stacker", "stacker")
	checkHorizonProvider(t, db)
}
