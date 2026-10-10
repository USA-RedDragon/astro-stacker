package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"
)

type Server struct {
	server        *http.Server
	metricsServer *http.Server
	pprofServer   *http.Server
	stopped       bool
	config        *config.Config
}

const defTimeout = 5 * time.Second

func NewServer(config *config.Config, appStore store.Store, schedulerDBStore store.Store, signer *previewer.Signer, broker *events.Broker, restacker middleware.Restacker, version string, extras Extras) *Server {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()

	writeTimeout := defTimeout
	if config.PProf.Enabled {
		writeTimeout = 60 * time.Second
	}

	applyMiddleware(r, config, appStore, schedulerDBStore, version, restacker)
	applyRoutes(r, signer, broker)
	applySchedulerRoutes(r.Group("/api/v1"), extras)
	applyWebUI(r)

	var metricsServer *http.Server
	var pprofServer *http.Server

	if config.Metrics.Enabled {
		metricsRouter := gin.New()
		applyMiddleware(metricsRouter, config, appStore, schedulerDBStore, version, nil)

		metricsRouter.GET("/metrics", gin.WrapH(promhttp.Handler()))
		metricsServer = &http.Server{
			Addr:              fmt.Sprintf("%s:%d", config.Metrics.Bind, config.Metrics.Port),
			ReadHeaderTimeout: defTimeout,
			WriteTimeout:      writeTimeout,
			Handler:           metricsRouter,
		}
	}

	if config.PProf.Enabled {
		pprofRouter := gin.New()
		applyMiddleware(pprofRouter, config, appStore, schedulerDBStore, version, nil)
		pprof.Register(pprofRouter)
		pprofServer = &http.Server{
			Addr:              fmt.Sprintf("%s:%d", config.PProf.Bind, config.PProf.Port),
			ReadHeaderTimeout: defTimeout,
			WriteTimeout:      writeTimeout,
			Handler:           pprofRouter,
		}
	}

	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", config.HTTP.Bind, config.HTTP.Port),
		ReadHeaderTimeout: defTimeout,
		WriteTimeout:      writeTimeout,
		Handler:           r,
	}
	// Event streams never end on their own: end them as shutdown starts, so
	// it doesn't wait them out until its timeout.
	server.RegisterOnShutdown(broker.Close)

	return &Server{
		server:        server,
		metricsServer: metricsServer,
		pprofServer:   pprofServer,
		config:        config,
	}
}

func applyMiddleware(r *gin.Engine, config *config.Config, appStore store.Store, schedulerDBStore store.Store, version string, restacker middleware.Restacker) {
	r.Use(gin.Recovery())
	r.Use(gin.Logger())

	r.TrustedPlatform = "X-Real-IP"

	err := r.SetTrustedProxies(config.HTTP.TrustedProxies)
	if err != nil {
		slog.Error("Failed to set trusted proxies", "error", err.Error())
	}

	var di = &middleware.DepInjection{
		Config:           config,
		AppStore:         appStore,
		SchedulerDBStore: schedulerDBStore,
		Version:          version,
		Restacker:        restacker,
	}

	r.Use(middleware.Inject(di))
}

func (s *Server) Start(ctx context.Context) error {
	waitGrp := sync.WaitGroup{}
	lc := net.ListenConfig{}
	if s.server != nil {
		listener, err := lc.Listen(ctx, "tcp", s.server.Addr)
		if err != nil {
			return err
		}
		waitGrp.Add(1)
		go func() {
			defer waitGrp.Done()
			if err := s.server.Serve(listener); err != nil && !s.stopped {
				slog.Error("HTTP server error", "error", err.Error())
			}
		}()
	}
	slog.Info("HTTP server started", "address", s.config.HTTP.Bind, "port", s.config.HTTP.Port)

	if s.config.Metrics.Enabled {
		if s.metricsServer != nil {
			metricsListener, err := lc.Listen(ctx, "tcp", s.metricsServer.Addr)
			if err != nil {
				return err
			}
			waitGrp.Add(1)
			go func() {
				defer waitGrp.Done()
				if err := s.metricsServer.Serve(metricsListener); err != nil && !s.stopped {
					slog.Error("Metrics server error", "error", err.Error())
				}
			}()
		}

		slog.Info("Metrics server started", "address", s.config.Metrics.Bind, "port", s.config.Metrics.Port)
	}

	if s.config.PProf.Enabled {
		if s.pprofServer != nil {
			pprofListener, err := lc.Listen(ctx, "tcp", s.pprofServer.Addr)
			if err != nil {
				return err
			}
			waitGrp.Add(1)
			go func() {
				defer waitGrp.Done()
				if err := s.pprofServer.Serve(pprofListener); err != nil && !s.stopped {
					slog.Error("PProf server error", "error", err.Error())
				}
			}()
		}

		slog.Info("PProf server started", "address", s.config.PProf.Bind, "port", s.config.PProf.Port)
	}

	go func() {
		waitGrp.Wait()
	}()
	return nil
}

func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.stopped = true

	errGrp := errgroup.Group{}
	if s.server != nil {
		errGrp.Go(func() error {
			return s.server.Shutdown(ctx)
		})
	}
	if s.metricsServer != nil {
		errGrp.Go(func() error {
			return s.metricsServer.Shutdown(ctx)
		})
	}
	if s.pprofServer != nil {
		errGrp.Go(func() error {
			return s.pprofServer.Shutdown(ctx)
		})
	}

	return errGrp.Wait()
}
