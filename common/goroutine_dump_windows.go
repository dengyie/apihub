//go:build windows

package common

// StartGoroutineDumpOnSignal 在 Windows 上没有 SIGUSR1，不提供信号触发的转储。
func StartGoroutineDumpOnSignal() {}
