package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/server/router"
	"gorm.io/gorm"
)

const healthCheckTimeout = 2 * time.Second

func init() {
	router.NewGroupRouter("/").
		AddRoute(
			router.NewRoute("/healthz", http.MethodGet).
				Handle(healthz),
		)
}

func healthz(c *gin.Context) {
	checkHealth(c, db.GetDB())
}

func checkHealth(c *gin.Context, conn *gorm.DB) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), healthCheckTimeout)
	defer cancel()
	var probe int
	if conn == nil || conn.WithContext(ctx).Raw("SELECT 1").Scan(&probe).Error != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
