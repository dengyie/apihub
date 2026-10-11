package common

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReplayableBodyReaderKeepsStorageLifecycleWithCaller(t *testing.T) {
	payload := []byte(`{"model":"test-model","input":"hello"}`)
	storage, err := CreateBodyStorage(payload)
	require.NoError(t, err)
	defer storage.Close()

	body := NewReplayableBodyReader(storage)
	assert.EqualValues(t, len(payload), body.Size())
	_, exposesCloser := any(body).(io.Closer)
	assert.False(t, exposesCloser, "the request body must not expose the storage closer")

	req, err := http.NewRequest(http.MethodPost, "https://example.com", body)
	require.NoError(t, err)
	require.NoError(t, req.Body.Close())

	replayBody, err := body.NewReader()
	require.NoError(t, err, "closing the HTTP request body must not close the storage")
	replay, err := io.ReadAll(replayBody)
	require.NoError(t, err)
	require.NoError(t, replayBody.Close())
	assert.Equal(t, payload, replay)

	require.NoError(t, storage.Close())
	_, err = body.NewReader()
	require.ErrorIs(t, err, ErrStorageClosed)
}

// truncatedBodyReader reproduces the production shape of a client upload cut
// off mid-body: some bytes arrive, then the stream dies with
// io.ErrUnexpectedEOF instead of a clean EOF.
type truncatedBodyReader struct {
	data []byte
	pos  int
}

func (r *truncatedBodyReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func TestCreateBodyStorageFromReaderWrapsTruncationWithByteCounts(t *testing.T) {
	content := []byte(`{"model":"glm-5.3-flash","messages":[`)
	storage, err := CreateBodyStorageFromReader(&truncatedBodyReader{data: content}, int64(len(content)+4096), 1<<20)
	require.Error(t, err)
	require.Nil(t, storage)

	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.True(t, IsRequestBodyIncompleteError(err))
	assert.Contains(t, err.Error(), "received ")
	assert.Contains(t, err.Error(), fmt.Sprintf("received %d of %d declared bytes", len(content), len(content)+4096))
}

func TestCreateBodyStorageFromReaderKeepsMalformedBodyUntagged(t *testing.T) {
	// A complete-but-garbage stream (clean EOF) must NOT be classified as a
	// transport abort: it is a genuine 400 invalid_request.
	storage, err := CreateBodyStorageFromReader(strings.NewReader("not json"), 8, 1<<20)
	require.NoError(t, err)
	require.NotNil(t, storage)
	require.NoError(t, storage.Close())
}

func TestIsRequestBodyIncompleteErrorShapes(t *testing.T) {
	assert.True(t, IsRequestBodyIncompleteError(io.ErrUnexpectedEOF))
	assert.True(t, IsRequestBodyIncompleteError(http.ErrBodyReadAfterClose))
	assert.True(t, IsRequestBodyIncompleteError(fmt.Errorf("wrapped: %w", io.ErrUnexpectedEOF)))
	assert.True(t, IsRequestBodyIncompleteError(&net.OpError{Op: "read", Err: syscall.ECONNRESET}))
	assert.False(t, IsRequestBodyIncompleteError(nil))
	assert.False(t, IsRequestBodyIncompleteError(io.EOF))
	assert.False(t, IsRequestBodyIncompleteError(errors.New("invalid JSON request body")))
}
