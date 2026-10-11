package loadbalancer

// 仅供测试观察内部状态的访问器。放在 _test.go 里，生产二进制不携带。

// EmptyStreamStreak 返回该条目当前连续空流次数。
func (t *Tracker) EmptyStreamStreak(channelID int, model string) int {
	if channelID <= 0 {
		return 0
	}
	return int(t.getBreaker(scopeKey(channelID, model)).consecutiveEmptyStreams.Load())
}
