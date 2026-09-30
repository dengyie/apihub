package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestSystemPerformanceCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origConfig := common.GetPerformanceMonitorConfig()
	origStatus := common.GetSystemStatus()
	defer func() {
		common.SetPerformanceMonitorConfig(origConfig)
		common.SetSystemStatus(origStatus)
	}()

	// 1. 正常负载，放行请求
	common.SetPerformanceMonitorConfig(common.PerformanceMonitorConfig{
		Enabled:         true,
		CPUThreshold:    80,
		MemoryThreshold: 80,
		DiskThreshold:   80,
	})
	common.SetSystemStatus(common.SystemStatus{
		CPUUsage:    20.0,
		MemoryUsage: 30.0,
		DiskUsage:   40.0,
	})

	router := gin.New()
	router.Use(SystemPerformanceCheck())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	router.POST("/v1/messages", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok", w.Body.String())

	// 2. CPU 超阈值拦截 OpenAI 路径，返回 503 并记录告警
	common.SetSystemStatus(common.SystemStatus{
		CPUUsage:    95.0,
		MemoryUsage: 30.0,
		DiskUsage:   40.0,
	})

	wOpenAI := httptest.NewRecorder()
	reqOpenAI := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	router.ServeHTTP(wOpenAI, reqOpenAI)
		assert.Equal(t, http.StatusServiceUnavailable, wOpenAI.Code)
		assert.Contains(t, wOpenAI.Body.String(), "system cpu overloaded")
		assert.Contains(t, wOpenAI.Body.String(), `"code":"system_cpu_overloaded"`)

		// 3. 内存超阈值拦截 Claude 路径，返回 Claude 错误格式
		common.SetSystemStatus(common.SystemStatus{
			CPUUsage:    20.0,
			MemoryUsage: 92.0,
			DiskUsage:   40.0,
		})

		wClaude := httptest.NewRecorder()
		reqClaude := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		router.ServeHTTP(wClaude, reqClaude)
		assert.Equal(t, http.StatusServiceUnavailable, wClaude.Code)
		assert.Contains(t, wClaude.Body.String(), "system memory overloaded")
		assert.Contains(t, wClaude.Body.String(), `"type":"new_api_error"`)

	// 4. 监控关闭时，高负载依然放行
	common.SetPerformanceMonitorConfig(common.PerformanceMonitorConfig{
		Enabled: false,
	})
	wBypass := httptest.NewRecorder()
	reqBypass := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	router.ServeHTTP(wBypass, reqBypass)
	assert.Equal(t, http.StatusOK, wBypass.Code)
}
