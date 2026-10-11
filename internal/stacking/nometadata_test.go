package stacking

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// A light uploaded moments ago whose Target Scheduler record has not
// arrived yet is looked at again in a minute; an older one is left for
// record to give RetryAfter.
func TestNoMetadataRetriesSoonAfterUpload(t *testing.T) {
	t.Parallel()
	p := &Pipeline{opts: DefaultPipelineOptions()}
	fresh := p.leftOut(candidate{frame: app.Frame{LastModified: time.Now().Add(-time.Minute)}}, app.StackStatusNoMetadata)
	if fresh.NextAttemptAt == nil || time.Until(*fresh.NextAttemptAt) > 2*time.Minute {
		t.Errorf("fresh light next attempt %v, want within two minutes", fresh.NextAttemptAt)
	}
	old := p.leftOut(candidate{frame: app.Frame{LastModified: time.Now().Add(-time.Hour)}}, app.StackStatusNoMetadata)
	if old.NextAttemptAt != nil {
		t.Errorf("old light next attempt %v, want RetryAfter from record", old.NextAttemptAt)
	}
}
