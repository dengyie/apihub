package middleware

import (
	"github.com/dengyie/apihub/common"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func CORS() gin.HandlerFunc {
	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	// 按 Fetch 规范 AllowAllOrigins 与 AllowCredentials=true 是矛盾组合（带凭据
	// 的请求会被浏览器拒绝 `Access-Control-Allow-Origin: *`），而本服务的浏览器
	// 侧消费者（share.mangoqwq.com 的 ai-hub、各类 Web 客户端）全部走 Bearer
	// token 鉴权，没有任何跨源流程依赖 cookie。关掉凭据授权，杜绝任意网页借
	// 凭据模式读取带会话的响应。
	config.AllowCredentials = false
	config.AllowMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{"*"}
	return cors.New(config)
}

func Version() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-New-Api-Version", common.Version)
		c.Next()
	}
}
