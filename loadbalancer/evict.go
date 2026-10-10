package loadbalancer

import (
	"sort"
	"time"
)

// 三张进程内注册表（熔断器 tracker、模型耗尽 exhaustion、禁用佐证
// corroboration）的键里都有**客户端可控**的部分：模型名来自请求体。因此
// 「只删过期条目」不足以构成上界 —— 一个窗口之内涌入足够多的互不相同的键，
// 过期清扫一条也删不掉，map 会继续长到攻击停止为止。
//
// tracker 之所以没这个问题，是因为它的键是 (渠道, 模型) 而渠道数由配置决定、
// 模型数有上限，键空间天然有界（见 breakerEvictThreshold 的注释）。另两张
// 不一样：exhaustion 的键**就是**模型名本身。
//
// 所以这里给出与过期清扫互补的第二道闸门：超过硬上限时按窗口起点从最旧开始
// 丢，直到回到上限内。它不改变任何语义 —— 被丢掉的条目本来就已过期或即将过期
// （清零后下一次调用会重建），丢掉的只是内存。
const (
	// corroborationCap 是佐证注册表的硬上限。
	corroborationCap = 16384
	// exhaustionCap 是模型耗尽注册表的硬上限。
	exhaustionCap = 4096
)

// dropOldestWhenOverCap 在过期清扫之后仍然超过硬上限时，按各条目的窗口起点
// 从最旧开始丢弃，直到回到上限以内。
//
// 返回被丢弃的条数，供调用方在日志/测试里观察。keys 由调用方在持锁状态下收集，
// 这里不碰 map 本身。
func dropOldestWhenOverCap[V any](keys []string, at func(V) time.Time, size map[string]V, cap int) int {
	if len(keys) <= cap {
		return 0
	}
	sort.Slice(keys, func(i, j int) bool {
		return at(size[keys[i]]).Before(at(size[keys[j]]))
	})
	dropped := len(keys) - cap
	for _, k := range keys[:dropped] {
		delete(size, k)
	}
	return dropped
}
