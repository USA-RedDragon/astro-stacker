package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveryreport"
	"github.com/gin-gonic/gin"
)

func applyReportRoutes(g *gin.RouterGroup, svc *discoveryreport.Service) {
	g.GET("/discoveries/candidates/:id/report", withReport(svc, reportGet))
	g.POST("/discoveries/candidates/:id/report/measure", withReport(svc, reportMeasure))
	g.PUT("/discoveries/candidates/:id/report/tns-name", withReport(svc, reportName))
	g.POST("/discoveries/candidates/:id/report/tns-send", withReport(svc, reportSend))
	g.POST("/discoveries/candidates/:id/report/tns-followup-send", withReport(svc, reportFollowupSend))
	g.GET("/discoveries/candidates/:id/report/bundle.zip", withReport(svc, reportBundle))
}

func withReport(svc *discoveryreport.Service, h func(*gin.Context, *discoveryreport.Service, int)) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "reports are not set up in this stacker"})
			return
		}
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: errIDNumber})
			return
		}
		h(c, svc, id)
	}
}

func reportError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, discoveryreport.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{errorKey: err.Error()})
	case errors.Is(err, discoveryreport.ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
	case errors.Is(err, discoveryreport.ErrTNS):
		c.JSON(http.StatusBadGateway, gin.H{errorKey: err.Error()})
	default:
		serverError(c, err)
	}
}

func reportGet(c *gin.Context, svc *discoveryreport.Service, id int) {
	r, err := svc.Report(c.Request.Context(), id)
	if err != nil {
		reportError(c, err)
		return
	}
	c.JSON(http.StatusOK, r)
}

func reportMeasure(c *gin.Context, svc *discoveryreport.Service, id int) {
	sp, err := svc.Measure(c.Request.Context(), id, c.Query("again") == "1")
	if err != nil {
		reportError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, sp)
}

func reportName(c *gin.Context, svc *discoveryreport.Service, id int) {
	var body struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: "send {\"name\": \"AT 2026xyz\"}"})
		return
	}
	if err := svc.SetName(c.Request.Context(), id, body.Name); err != nil {
		reportError(c, err)
		return
	}
	reportGet(c, svc, id)
}

func reportSend(c *gin.Context, svc *discoveryreport.Service, id int) {
	reply, err := svc.SendTNS(c.Request.Context(), id)
	if err != nil {
		reportError(c, err)
		return
	}
	c.JSON(http.StatusOK, reply)
}

func reportFollowupSend(c *gin.Context, svc *discoveryreport.Service, id int) {
	reply, err := svc.SendFollowup(c.Request.Context(), id)
	if err != nil {
		reportError(c, err)
		return
	}
	c.JSON(http.StatusOK, reply)
}

func reportBundle(c *gin.Context, svc *discoveryreport.Service, id int) {
	name, b, err := svc.Bundle(c.Request.Context(), id)
	if err != nil {
		reportError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Data(http.StatusOK, "application/zip", b)
}
