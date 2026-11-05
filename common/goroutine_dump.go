package common

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"
)

// WriteGoroutineDump 把当前全部 goroutine 的调用栈写入 dir 下的新文件，返回文件路径。
//
// 这是「进程看似卡住」时唯一可靠的取证手段：SIGQUIT 虽然也会打印栈，但会顺带
// 杀掉进程（还写到 stdout，而 stdout 是否可读取决于 supervisord 是否健在）；
// ENABLE_PPROF 则监听 0.0.0.0:8005，不能在生产常开。这里只写本地文件、进程照常服务。
func WriteGoroutineDump(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, fmt.Sprintf("goroutine-%s-%d.txt", time.Now().Format("20060102-150405"), os.Getpid()))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	if err := pprof.Lookup("goroutine").WriteTo(f, 2); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}
