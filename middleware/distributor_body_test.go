package middleware

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dengyie/apihub/i18n"
	jsplugin "github.com/dengyie/apihub/pkg/jsplugin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// truncatedUploadBody reproduces the 2026-10-12 production failure: a client
// (ZCode, ~120s request deadline over a slow uplink) gives up mid-upload, so
// the gateway's body read dies with io.ErrUnexpectedEOF long before the
// declared Content-Length arrives.
type truncatedUploadBody struct {
	data []byte
	pos  int
}

func (r *truncatedUploadBody) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func serveDistributeBody(t *testing.T, body io.Reader, contentLength int64, contentType string) *httptest.ResponseRecorder {
	require.NoError(t, i18n.Init())
	router := gin.New()
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Request.ContentLength = contentLength
		c.Request.Header.Set("Content-Type", contentType)
		c.Request.Header.Set("Accept-Language", "en")
		c.Next()
	}, Distribute(), func(c *gin.Context) {
		t.Error("body-failed requests must stop before the relay handler")
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// A transport abort during body read must be answered with the honest 408 +
// client_aborted, never 400 invalid_request: ZCode treated the old 400 as a
// non-retryable request defect ("Turn execution failed ... retryable=false").
func TestDistributeClassifiesTruncatedBodyUploadAsRequestTimeout(t *testing.T) {
	payload := []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`)
	recorder := serveDistributeBody(t, &truncatedUploadBody{data: payload}, int64(len(payload))+8192, "application/json")

	require.Equal(t, http.StatusRequestTimeout, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"client_aborted"`)
	assert.Contains(t, recorder.Body.String(), "request body incomplete: received")
	assert.NotContains(t, recorder.Body.String(), "Invalid request: Invalid request:")
	assert.NotContains(t, recorder.Body.String(), "invalid_request")
}

// A fully received but malformed payload is a genuine client error: it keeps
// 400, and the "Invalid request:" prefix must appear exactly once (the wrap
// used to be applied both in getModelFromRequest and again at the Distribute
// exit, producing "Invalid request: Invalid request: ...").
func TestDistributeKeepsSingleInvalidRequestPrefixForMalformedBody(t *testing.T) {
	recorder := serveDistributeBody(t, strings.NewReader("this is not json"), 16, "application/json")

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "Invalid request:"))
	assert.Contains(t, recorder.Body.String(), "invalid JSON request body")
	assert.Contains(t, recorder.Body.String(), `"code":"invalid_request"`)
}

// The double prefix is a regression guard on the message shape itself.
func TestDistributeBodyErrorMessageHasNoDoublePrefix(t *testing.T) {
	recorder := serveDistributeBody(t, strings.NewReader(`{"model":`), 9, "application/json")
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	body := recorder.Body.String()
	assert.Equal(t, 1, strings.Count(body, "Invalid request:"),
		fmt.Sprintf("expected a single prefix, got: %s", body))
}

// POST /v1/responses is a host protocol path: PinTaskPluginEndpoint parses the
// model itself and used to answer a truncated upload with a bare 400. The
// classification must survive that exit too.
func TestPinTaskPluginEndpointClassifiesTruncatedBodyAsRequestTimeout(t *testing.T) {
	require.NoError(t, i18n.Init())
	_, err := jsplugin.DefaultRegistry.Register(genericTaskPluginSource, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("generic-entry-test") })

	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.Header.Set("Accept-Language", "en")
		c.Next()
	}, PinTaskPluginEndpoint(), func(c *gin.Context) {
		t.Error("aborted uploads must stop before protocol routing continues")
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/responses",
		&truncatedUploadBody{data: []byte(`{"model":"doc","input":"hi"}`)})
	request.Header.Set("Content-Type", "application/json")
	// 声明长度远大于实际到达字节，复现生产截断形态（客户端声明了 Content-Length）
	request.ContentLength = 4096
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusRequestTimeout, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"client_aborted"`)
	assert.Contains(t, recorder.Body.String(), "request body incomplete: received")
	assert.NotContains(t, recorder.Body.String(), "Invalid task protocol request")
}
