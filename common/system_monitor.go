package common

import (
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/mem"
)

// DiskSpaceInfo 磁盘空间信息
type DiskSpaceInfo struct {
	// 总空间（字节）
	Total uint64 `json:"total"`
	// 可用空间（字节）
	Free uint64 `json:"free"`
	// 已用空间（字节）
	Used uint64 `json:"used"`
	// 使用百分比
	UsedPercent float64 `json:"used_percent"`
}

// SystemStatus 系统状态信息
type SystemStatus struct {
	CPUUsage    float64
	MemoryUsage float64
	DiskUsage   float64
}

var latestSystemStatus atomic.Value

func init() {
	latestSystemStatus.Store(SystemStatus{})
}

// StartSystemMonitor 启动系统监控
func StartSystemMonitor() {
	go func() {
		var prevCPU cpu.TimesStat
		for {
			config := GetPerformanceMonitorConfig()
			if !config.Enabled {
				time.Sleep(30 * time.Second)
				continue
			}

			prevCPU = updateSystemStatus(prevCPU)
			time.Sleep(5 * time.Second)
		}
	}()
}

// updateSystemStatus 采样一次系统负载，返回本次 CPU 时间供下一轮求差。
// 首轮 prevCPU 为零值，CPU 读数按开机以来的平均算，下一轮起就是 5 秒窗口。
func updateSystemStatus(prevCPU cpu.TimesStat) cpu.TimesStat {
	var status SystemStatus

	curCPU := prevCPU
	if times, err := cpu.Times(false); err == nil && len(times) > 0 {
		curCPU = times[0]
		status.CPUUsage = cpuBusyPercent(prevCPU, curCPU)
	}

	// Memory
	memInfo, err := mem.VirtualMemory()
	if err == nil {
		status.MemoryUsage = memInfo.UsedPercent
	}

	// Disk
	diskInfo := GetDiskSpaceInfo()
	if diskInfo.Total > 0 {
		status.DiskUsage = diskInfo.UsedPercent
	}

	latestSystemStatus.Store(status)
	return curCPU
}

// cpuBusyPercent 返回两次采样之间 CPU 真正在执行指令的时间占比。
//
// 只有 user/nice/system/irq/softirq 计为忙碌。iowait 是 CPU 空闲着等 I/O，steal
// 是宿主机把时间片给了别的虚机，两者都不是本机在干活。gopsutil 的 cpu.Percent
// 把它们也计为忙碌，于是 NFS 卡死或宿主机超卖时读数顶到 100%，性能保护把所有
// 中继请求 503 掉——而负载根本不在 CPU 上，拒绝请求卸不掉它。
func cpuBusyPercent(prev, cur cpu.TimesStat) float64 {
	busy := (cur.User + cur.Nice + cur.System + cur.Irq + cur.Softirq) -
		(prev.User + prev.Nice + prev.System + prev.Irq + prev.Softirq)
	idle := (cur.Idle + cur.Iowait + cur.Steal) - (prev.Idle + prev.Iowait + prev.Steal)
	if busy+idle <= 0 {
		return 0
	}
	return min(100, max(0, busy/(busy+idle)*100))
}

// GetSystemStatus 获取当前系统状态
func GetSystemStatus() SystemStatus {
	return latestSystemStatus.Load().(SystemStatus)
}

// SetSystemStatus 覆盖当前系统状态采样。测试注入超阈值负载用。
func SetSystemStatus(status SystemStatus) {
	latestSystemStatus.Store(status)
}
