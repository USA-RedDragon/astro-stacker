package middleware

import (
	"github.com/USA-RedDragon/pixinsight-worker/internal/config"
	"github.com/USA-RedDragon/pixinsight-worker/internal/store"
	"github.com/gin-gonic/gin"
)

type DepInjection struct {
	Config           *config.Config
	AppStore         store.Store
	SchedulerDBStore store.Store
	Version          string
}

const DepInjectionKey = "DepInjection"

func Inject(inj *DepInjection) gin.HandlerFunc {
	return func(c *gin.Context) {
		inj.AppStore = inj.AppStore.WithContext(c.Request.Context())
		inj.SchedulerDBStore = inj.SchedulerDBStore.WithContext(c.Request.Context())
		c.Set(DepInjectionKey, inj)
		c.Next()
	}
}
