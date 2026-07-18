package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// MediaHandler 处理本地存储的媒体文件读取和上传。
// 仅在 storage_driver=local 时被注册到路由。
//
// 路由：
//
//	GET  /api/media/*key?expires=<unix>&signature=<hex>  — 下载（支持 Range/HEAD）
//	PUT  /api/media/*key?expires=<unix>&signature=<hex>  — 上传
//
// 鉴权：使用 HMAC-SHA256 签名 URL，不依赖 JWT（与 S3 presigned URL 语义一致）。
type MediaHandler struct {
	storage *repository.LocalImageStorage
}

// NewMediaHandler 构造媒体 handler。仅在本地存储模式下注入。
func NewMediaStorageHandler(storage service.ImageObjectStorage) *MediaHandler {
	// 类型断言：只有 local 驱动才注册此 handler
	local, ok := storage.(*repository.LocalImageStorage)
	if !ok {
		return nil
	}
	return &MediaHandler{storage: local}
}

// ServeMedia 处理 GET/HEAD/PUT 请求。
// 路由使用 /*key 通配，key 从 c.Param("key") 获取。
func (h *MediaHandler) ServeMedia(c *gin.Context) {
	if h == nil || h.storage == nil {
		c.Status(http.StatusNotFound)
		return
	}

	// 从 URL 路径提取 object key（去除前导 /）
	key := strings.TrimPrefix(c.Param("key"), "/")
	if key == "" {
		c.Status(http.StatusNotFound)
		return
	}

	// 从 query 解析签名参数
	expiresStr := c.Query("expires")
	signature := c.Query("signature")
	if expiresStr == "" || signature == "" {
		c.Status(http.StatusForbidden)
		return
	}

	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		c.Status(http.StatusForbidden)
		return
	}

	// 检查过期
	if time.Now().Unix() > expires {
		c.Status(http.StatusForbidden)
		return
	}

	method := c.Request.Method
	switch method {
	case http.MethodGet, http.MethodHead:
		// 验证签名
		if !h.storage.VerifySignature(method, key, expires, signature) {
			c.Status(http.StatusForbidden)
			return
		}
		h.storage.ServeFile(c.Writer, c.Request, key)

	case http.MethodPut:
		// 验证签名
		if !h.storage.VerifySignature(method, key, expires, signature) {
			c.Status(http.StatusForbidden)
			return
		}
		contentType := c.Query("content_type")
		if contentType == "" {
			contentType = c.GetHeader("Content-Type")
		}
		size, err := h.storage.ReceiveUpload(c.Request, key, contentType)
		if err != nil {
			// 不泄露文件系统路径
			if strings.Contains(err.Error(), "disk space") {
				c.JSON(http.StatusInsufficientStorage, gin.H{"error": "insufficient disk space"})
				return
			}
			if strings.Contains(err.Error(), "already exists") {
				c.JSON(http.StatusConflict, gin.H{"error": "object already exists"})
				return
			}
			if strings.Contains(err.Error(), "traversal") || strings.Contains(err.Error(), "path") {
				c.Status(http.StatusNotFound)
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "upload failed"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"size": size})

	default:
		c.Status(http.StatusMethodNotAllowed)
	}
}
