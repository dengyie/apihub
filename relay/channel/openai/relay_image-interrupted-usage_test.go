package openai

import (
	"errors"
	"testing"

	"github.com/dengyie/apihub/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 图像流的断流与文本流同构，但判据换了：没有文本可数，改数真正生成出来的张数。
//
// 这里尤其要小心 usage 帧 —— 它带着一整段 prompt 计数，只看「拿到 usage 了」
// 就返回它，一次失败的请求反而会被按整段 prompt 收一遍全款。

func imageStreamTestSetup(t *testing.T) {
	t.Helper()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
}

// errorAfterReader 先吐出 chunk，随后返回错误 —— 上游连接被重置的形状。
// 图像流的 EOF 不会判成断流（它没有 RequireTerminal），真正能让 streamErr
// 非 nil 的就是这类读/写错误，也正是 interruptedImageUsage 要处理的场合。
type errorAfterReader struct {
	chunk []byte
	done  bool
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("connection reset by peer")
	}
	r.done = true
	return copy(p, r.chunk), nil
}

func (r *errorAfterReader) Close() error { return nil }

// 断流前已经生成出一张图并写给了客户端，这一张必须计费。
func TestOpenaiImageTruncatedStreamReturnsDeliveredUsage(t *testing.T) {
	imageStreamTestSetup(t)
	body := `data: {"type":"image_generation.completed","b64_json":"first"}` + "\n\n" +
		`data: {"type":"usage","usage":{"input_tokens":300,"output_tokens":1400,"total_tokens":1700}}` + "\n\n"
	c, _, resp, info := newImageTestContext(t, body, "text/event-stream", true)
	resp.Body = &errorAfterReader{chunk: []byte(body)}

	usage, err := OpenaiImageStreamHandler(c, info, resp)

	require.NotNil(t, err, "本用例的前提就是流确实断了")
	require.NotNil(t, usage, "已经生成并交付的图必须随错误一起返回，否则这一次白送")
	assert.Greater(t, usage.TotalTokens, 0)
}

// 反向守卫：一张图都没生成出来时必须返回 nil。上游完全可能只送来一个错误帧
// 外加 usage 帧（带完整 prompt 计数），把它当成「有产出」就会按 8000 误收。
func TestOpenaiImageErrorFrameOnlyIsNotBilled(t *testing.T) {
	imageStreamTestSetup(t)
	body := `data: {"type":"error","error":{"message":"upstream overloaded","type":"server_error"}}` + "\n\n" +
		`data: {"type":"usage","usage":{"input_tokens":8000,"output_tokens":0,"total_tokens":8000}}` + "\n\n"
	c, _, resp, info := newImageTestContext(t, body, "text/event-stream", true)
	resp.Body = &errorAfterReader{chunk: []byte(body)}

	usage, err := OpenaiImageStreamHandler(c, info, resp)

	require.NotNil(t, err)
	assert.Nil(t, usage,
		"一张图都没生成 = 客户端什么都没拿到，不该按 8000 token 的 prompt 计费")
}
