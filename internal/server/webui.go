package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

func isWebPage(p string) bool {
	switch p {
	case "/now", "/tonight", "/add", "/mosaics", "/templates", "/history", "/catalogues", "/finder", "/collabs":
		return true
	}
	return false
}

func webRedirect(base string, u *url.URL) string {
	p := strings.TrimRight(u.Path, "/")
	q := u.Query()
	switch {
	case p == "/targets":
		p = "/"
		q.Set("view", "list")
	case strings.HasPrefix(p, "/targets/"):
		id := strings.TrimPrefix(p, "/targets/")
		if t := q.Get("target"); t != "" {
			p = "/target/" + url.PathEscape(t)
			q.Del("target")
		} else {
			p = "/project/" + url.PathEscape(id)
		}
	case strings.HasPrefix(p, "/mosaics/"):
	case isWebPage(p):
	default:
		p = "/"
	}
	s := base + p
	if len(q) > 0 {
		s += "?" + q.Encode()
	}
	return s
}

func applyWebUI(r *gin.Engine, base string) {
	base = strings.TrimRight(base, "/")
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if base == "" || strings.HasPrefix(p, "/api/") || p == "/api" ||
			(c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) {
			c.JSON(http.StatusNotFound, gin.H{errorKey: "not found"})
			return
		}
		c.Redirect(http.StatusFound, webRedirect(base, c.Request.URL))
	})
}
