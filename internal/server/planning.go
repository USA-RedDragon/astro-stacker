package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func loadGoalSettings(ctx context.Context, appDB, sched *gorm.DB) (map[goals.Key]goals.Goal, error) {
	if appDB == nil {
		return map[goals.Key]goals.Goal{}, nil
	}
	gs, _, err := goals.LoadGoals(ctx, appDB, sched)
	return gs, err
}

func applyPlanningRoutes(g *gin.RouterGroup) {
	g.GET("/goals/mask", goalMaskRoute)
	g.GET("/goals/mask/info", goalMaskInfoRoute)
	g.GET("/planning", func(c *gin.Context) {
		s, ok := planningSnapshot(c)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, s)
	})
	g.GET("/planning/projects/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "bad project id"})
			return
		}
		s, ok := planningSnapshot(c)
		if !ok {
			return
		}
		p, found := s.Project(id)
		if !found {
			c.JSON(http.StatusNotFound, gin.H{errorKey: "no such project"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"project": p, "rules": s.Rules, "sets": s.Sets, "templates": s.Templates})
	})
	g.POST("/planning/applyset/draft", func(c *gin.Context) {
		var body struct {
			SetID     string `json:"setId"`
			Mode      string `json:"mode"`
			TargetIDs []int  `json:"targetIds"`
			Desired   int    `json:"desired"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		s, ok := planningSnapshot(c)
		if !ok {
			return
		}
		d, err := s.DraftApplySet(body.SetID, body.Mode, body.TargetIDs, body.Desired, schedcmd.NewID)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{errorKey: err.Error(), "draft": d})
			return
		}
		c.JSON(http.StatusOK, d)
	})
	g.POST("/planning/projects/draft", func(c *gin.Context) {
		var body planning.ProjectDraft
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		s, ok := planningSnapshot(c)
		if !ok {
			return
		}
		p, err := s.DraftProject(body, schedcmd.NewID)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, p)
	})
}

func planningSnapshot(c *gin.Context) (*planning.Snapshot, bool) {
	di, ok := depInjection(c)
	if !ok {
		return nil, false
	}
	if di.SchedulerDBStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "no scheduler database"})
		return nil, false
	}
	ctx := c.Request.Context()
	sched := di.SchedulerDBStore.DB()
	var appDB *gorm.DB
	if di.AppStore != nil {
		appDB = di.AppStore.DB()
	}
	gs, err := loadGoalSettings(ctx, appDB, sched)
	if err != nil {
		gs = map[goals.Key]goals.Goal{}
	}
	in := planning.Inputs{Goals: gs, ObjectsByGUID: map[string][]string{}}
	if appDB != nil {
		if byObject, err := goals.ObjectGUIDs(ctx, appDB, sched); err == nil {
			for obj, guid := range byObject {
				in.ObjectsByGUID[guid] = append(in.ObjectsByGUID[guid], obj)
			}
		}
	}
	s, err := planning.Load(ctx, sched, appDB, in)
	if err == nil {
		f := sky.Frame{FocalLength: 405, PixelSize: 3.76, WidthPx: 6248, HeightPx: 4176}
		if di.Config != nil && di.Config.Discover.FocalLength > 0 {
			f = sky.Frame{FocalLength: di.Config.Discover.FocalLength, PixelSize: di.Config.Discover.PixelSize, WidthPx: di.Config.Discover.SensorWidth, HeightPx: di.Config.Discover.SensorHeight}
		}
		s.Frame = planning.Frame{WidthDeg: f.WidthDeg(), HeightDeg: f.HeightDeg(), Scale: f.Scale()}
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, context.Canceled) {
			status = http.StatusRequestTimeout
		}
		c.JSON(status, gin.H{errorKey: err.Error()})
		return nil, false
	}
	return s, true
}
