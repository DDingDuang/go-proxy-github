package gateway

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed web/index.html
var indexHTML []byte

// handleIndex 管理面板首页(嵌入式单页, 无外部资源依赖)
func (g *Gateway) handleIndex(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
}
