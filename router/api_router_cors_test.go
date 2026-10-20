package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Browser clients on share.mangoqwq.com fetch public /api endpoints
// cross-origin, so every /api response must carry CORS headers. Regression
// for the production incident where GET /api/status returned 200 without
// access-control-allow-origin because SetApiRouter registered its routes
// before SetRelayRouter installed the engine-wide CORS middleware: Gin's
// Use() only affects routes registered after it, so /api/* was left bare.
func TestApiRouterPublicEndpointsSendCorsHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)

	paths := []string{"/api/status", "/api/about", "/api/notice"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Origin", "https://share.mangoqwq.com")
			engine.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, "expected route to exist")
			require.NotEmpty(t, recorder.Header().Get("Access-Control-Allow-Origin"),
				"%s response must carry Access-Control-Allow-Origin for browser cross-origin reads", path)
		})
	}

	preflight := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "/api/status", nil)
	request.Header.Set("Origin", "https://share.mangoqwq.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	engine.ServeHTTP(preflight, request)
	require.Equal(t, http.StatusNoContent, preflight.Code)
	require.NotEmpty(t, preflight.Header().Get("Access-Control-Allow-Origin"))
}
