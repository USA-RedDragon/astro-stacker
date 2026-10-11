package middleware

import (
	"context"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/gin-gonic/gin"
)

type DepInjection struct {
	Config           *config.Config
	AppStore         store.Store
	SchedulerDBStore store.Store
	Version          string
	// Restacker is the stacking pipeline, nil when stacking is off.
	Restacker Restacker
}

// Restacker stacks a target again from scratch.
type Restacker interface {
	Restack(ctx context.Context, object string) error
	Scores(ctx context.Context) (map[string]quality.SubScore, error)
}

const DepInjectionKey = "DepInjection"

func Inject(inj *DepInjection) gin.HandlerFunc {
	return func(c *gin.Context) {
		di := *inj
		if di.AppStore != nil {
			di.AppStore = di.AppStore.WithContext(c.Request.Context())
		}
		if di.SchedulerDBStore != nil {
			di.SchedulerDBStore = di.SchedulerDBStore.WithContext(c.Request.Context())
		}
		c.Set(DepInjectionKey, &di)
		c.Next()
	}
}
