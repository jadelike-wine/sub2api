package routes

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterCommonRoutes 注册通用路由（健康检查、状态等）
func RegisterCommonRoutes(r *gin.Engine) {
	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Claude Code 遥测日志（忽略，直接返回200）
	r.POST("/api/event_logging/batch", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Setup status endpoint (always returns needs_setup: false in normal mode)
	// This is used by the frontend to detect when the service has restarted after setup
	r.GET("/setup/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"data": gin.H{
				"needs_setup": false,
				"step":        "completed",
			},
		})
	})
}

// RegisterMediaRoutes 注册本地媒体文件路由（无 JWT 鉴权，使用 HMAC 签名 URL）。
// 仅在 storage_driver=local 且 MediaHandler 非 nil 时注册。
// 路由路径：/api/media/*key
func RegisterMediaRoutes(r *gin.Engine, h *handler.MediaHandler) {
	if h == nil {
		return
	}
	media := r.Group("/api/media")
	{
		media.GET("/*key", h.ServeMedia)
		media.HEAD("/*key", h.ServeMedia)
		media.PUT("/*key", h.ServeMedia)
	}
}
