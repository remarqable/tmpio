package controllers

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/obs"
)

// Healthz is liveness: no dependencies.
func (d *Deps) Healthz(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) }

// Readyz is readiness: checks the database without exposing raw errors.
func (d *Deps) Readyz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := db.Ping(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "db": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Metrics exposes the in-process registry to operators only: a METRICS_TOKEN
// bearer when configured, otherwise loopback clients.
func (d *Deps) Metrics(c *gin.Context) {
	if d.Cfg.MetricsToken != "" {
		if c.GetHeader("Authorization") != "Bearer "+d.Cfg.MetricsToken {
			c.Status(http.StatusNotFound)
			return
		}
	} else {
		ip := net.ParseIP(c.ClientIP())
		if ip == nil || !ip.IsLoopback() {
			c.Status(http.StatusNotFound)
			return
		}
	}
	c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	obs.Default.Write(c.Writer)
}

// Robots disallows crawling of owner, auth, API and sharing routes without listing tokens.
func (d *Deps) Robots(c *gin.Context) {
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusOK, "User-agent: *\nDisallow: /app\nDisallow: /auth\nDisallow: /oauth\nDisallow: /api\nDisallow: /mcp\nDisallow: /s/\nDisallow: /login\n")
}
