package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func backfillReport(ctx context.Context, appDB *gorm.DB, live func() goals.BackfillLive) *goals.Backfill {
	var l goals.BackfillLive
	if live != nil {
		l = live()
	}
	b, err := goals.CountBackfill(ctx, appDB, l)
	if err != nil {
		return nil
	}
	return &b
}

func applyPlanningRoutes(g *gin.RouterGroup, backfill func() goals.BackfillLive, cmds *schedcmd.Service) {
	g.GET("/goals/mask", goalMaskRoute)
	g.GET("/goals/mask/info", goalMaskInfoRoute)
	g.GET("/planning", func(c *gin.Context) {
		s, ok := planningSnapshot(c, backfill)
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
		s, ok := planningSnapshot(c, backfill)
		if !ok {
			return
		}
		p, found := s.Project(id)
		if !found {
			c.JSON(http.StatusNotFound, gin.H{errorKey: "no such project"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"project": p, "rules": s.Rules, "sets": s.Sets, "templates": s.Templates, "defaults": s.Defaults})
	})
	g.GET("/planning/rule-weights", func(c *gin.Context) {
		s, ok := planningSnapshot(c, backfill)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, s.GlobalWeights)
	})
	g.POST("/planning/rule-weights/preview", func(c *gin.Context) {
		var body struct {
			Weights map[string]float64 `json:"weights"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		s, ok := planningSnapshot(c, backfill)
		if !ok {
			return
		}
		g, err := s.GlobalWith(body.Weights)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, g)
	})
	g.POST("/planning/rule-weights/apply", func(c *gin.Context) {
		applyGlobalWeights(c, backfill, cmds)
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
		s, ok := planningSnapshot(c, backfill)
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
	g.POST("/planning/filters/draft", func(c *gin.Context) {
		var body struct {
			TargetIDs []int    `json:"targetIds"`
			Filters   []string `json:"filters"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		s, ok := planningSnapshot(c, backfill)
		if !ok {
			return
		}
		d, err := s.DraftFilters(body.TargetIDs, body.Filters, schedcmd.NewID)
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
		s, ok := planningSnapshot(c, backfill)
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

func planningSnapshot(c *gin.Context, backfill func() goals.BackfillLive) (*planning.Snapshot, bool) {
	di, ok := depInjection(c)
	if !ok {
		return nil, false
	}
	if di.SchedulerDBStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "no scheduler database"})
		return nil, false
	}
	var appDB *gorm.DB
	if di.AppStore != nil {
		appDB = di.AppStore.DB()
	}
	s, err := LoadPlanningSnapshot(c.Request.Context(), appDB, di.SchedulerDBStore.DB(), backfill)
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

// LoadPlanningSnapshot reads the scheduler and app state planning drafts
// from.
func LoadPlanningSnapshot(ctx context.Context, appDB, sched *gorm.DB, backfill func() goals.BackfillLive) (*planning.Snapshot, error) {
	in, err := planning.AppInputs(ctx, appDB, sched)
	if err != nil {
		return nil, fmt.Errorf("load goal settings: %w", err)
	}
	s, err := planning.Load(ctx, sched, appDB, in)
	if err != nil {
		return nil, err
	}
	rig, rerr := rigsource.Measure(ctx, appDB, sched, time.Now())
	s.Frame = planning.FrameFromRig(rig)
	if rerr != nil {
		msg := "the rig could not be measured: " + rerr.Error()
		s.Frame = planning.FrameFromRig(rigsource.Empty())
		s.Frame.Reason = &msg
	}
	s.Backfill = backfillReport(ctx, appDB, backfill)
	return s, nil
}

type globalWeightsResult struct {
	Global   *schedcmd.Record       `json:"global,omitempty"`
	Projects []schedcmd.Record      `json:"projects"`
	Failed   []string               `json:"failed"`
	Weights  planning.GlobalWeights `json:"weights"`
}

func applyGlobalWeights(c *gin.Context, backfill func() goals.BackfillLive, cmds *schedcmd.Service) {
	if cmds == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "Scheduler commands are not configured."})
		return
	}
	var body struct {
		Weights    map[string]float64 `json:"weights"`
		ProjectIDs []int              `json:"projectIds"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return
	}
	s, ok := planningSnapshot(c, backfill)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	out := globalWeightsResult{Projects: []schedcmd.Record{}, Failed: []string{}}
	if len(body.Weights) > 0 {
		p, err := s.GlobalChanges(body.Weights)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		if len(p.Changes) > 0 {
			raw, err := json.Marshal(p)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
				return
			}
			r, err := cmds.Submit(ctx, schedcmd.KindRuleWeightGlobal, raw, author(c))
			if err != nil {
				commandError(c, err)
				return
			}
			out.Global = &r
			if s, ok = planningSnapshot(c, backfill); !ok {
				return
			}
		}
	}
	edits, err := s.SyncEdits(body.ProjectIDs)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{errorKey: err.Error()})
		return
	}
	for _, e := range edits {
		raw, err := json.Marshal(e)
		if err == nil {
			var r schedcmd.Record
			if r, err = cmds.Submit(ctx, schedcmd.KindRuleWeightEdit, raw, author(c)); err == nil {
				out.Projects = append(out.Projects, r)
				continue
			}
		}
		out.Failed = append(out.Failed, e.ProjectName+": "+err.Error())
	}
	out.Weights = s.GlobalWeights
	c.JSON(http.StatusAccepted, out)
}
