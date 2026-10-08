package loadbalancer

import (
	"encoding/json"
	"fmt"
	"hash/fnv"

	"github.com/dengyie/apihub/relaykit/dto"
)

// StickyKeyFromRequest 从请求中提取用于一致性 hash 的 key（prompt 前缀）。
// 相同 prompt 前缀的请求会路由到同一渠道，提高上游 prompt cache 命中率。
// 返回空字符串表示无法提取，调用方应回退到普通选择。
func StickyKeyFromRequest(request any) string {
	var prefixBytes []byte

	switch r := request.(type) {
	case *dto.GeneralOpenAIRequest:
		// OpenAI: 取第一条消息（通常是 system）
		if len(r.Messages) > 0 {
			if b, err := json.Marshal(r.Messages[0]); err == nil {
				prefixBytes = b
			}
		}
	case *dto.ClaudeRequest:
		// Claude: 优先 system，其次第一条消息
		if r.System != nil {
			if b, err := json.Marshal(r.System); err == nil {
				prefixBytes = b
			}
		} else if len(r.Messages) > 0 {
			if b, err := json.Marshal(r.Messages[0]); err == nil {
				prefixBytes = b
			}
		}
	default:
		return ""
	}

	if len(prefixBytes) == 0 {
		return ""
	}
	// 只取前 2KB 做 hash，避免大 prompt 的性能开销
	if len(prefixBytes) > 2048 {
		prefixBytes = prefixBytes[:2048]
	}
	h := fnv.New32a()
	h.Write(prefixBytes)
	return fmt.Sprintf("%x", h.Sum32())
}

// StickyIndex 根据 sticky key 从 n 个候选中选一个索引。
// 相同 key 总是返回相同索引（n 不变时）。
// 返回 -1 表示无有效 sticky key，调用方用普通选择。
func StickyIndex(stickyKey string, n int) int {
	if n <= 0 || stickyKey == "" {
		return -1
	}
	h := fnv.New32a()
	h.Write([]byte(stickyKey))
	return int(h.Sum32() % uint32(n))
}
