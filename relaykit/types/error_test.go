package types

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type customTestError struct {
	Code int
}

func (e *customTestError) Error() string {
	return "custom test error"
}

func TestNewOpenAIError_PreservesUnderlyingError(t *testing.T) {
	orig := &customTestError{Code: 42}
	apiErr := NewOpenAIError(orig, ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	require.NotNil(t, apiErr)

	var target *customTestError
	assert.True(t, errors.As(apiErr, &target), "errors.As should find underlying customTestError")
	assert.Equal(t, 42, target.Code)
	assert.True(t, errors.Is(apiErr, orig), "errors.Is should match original error")
}
