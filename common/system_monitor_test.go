package common

import (
	"testing"

	"github.com/shirou/gopsutil/cpu"
	"github.com/stretchr/testify/assert"
)

func TestCPUBusyPercentIgnoresIOWaitAndSteal(t *testing.T) {
	prev := cpu.TimesStat{User: 1000, System: 300, Idle: 5000, Iowait: 200, Steal: 100}
	tests := []struct {
		name string
		cur  cpu.TimesStat
		want float64
	}{
		{
			// NFS 卡死：CPU 几乎全在等 I/O，cpu.Percent 会读成 99%
			name: "iowait dominated",
			cur:  cpu.TimesStat{User: 1001, System: 301, Idle: 5002, Iowait: 396, Steal: 100},
			want: 1,
		},
		{
			name: "steal dominated",
			cur:  cpu.TimesStat{User: 1002, System: 300, Idle: 5000, Iowait: 200, Steal: 298},
			want: 1,
		},
		{
			name: "real cpu work",
			cur:  cpu.TimesStat{User: 1150, System: 330, Nice: 10, Irq: 5, Softirq: 5, Idle: 5050, Iowait: 200, Steal: 100},
			want: 80,
		},
		{
			name: "no time elapsed",
			cur:  prev,
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, cpuBusyPercent(prev, tt.cur), 0.01)
		})
	}
}
