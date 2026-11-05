//go:build !windows

package common

import (
	"os"
	"os/signal"
	"syscall"
)

// StartGoroutineDumpOnSignal 让进程在收到 SIGUSR1 时把全部 goroutine 栈写到日志目录，
// 不中断服务。线上取证：kill -USR1 <pid>，再看 SysLog 打出的文件路径。
func StartGoroutineDumpOnSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGUSR1)
	go func() {
		for range sig {
			path, err := WriteGoroutineDump(*LogDir)
			if err != nil {
				SysError("goroutine dump failed: " + err.Error())
				continue
			}
			SysLog("goroutine dump written: " + path)
		}
	}()
}
