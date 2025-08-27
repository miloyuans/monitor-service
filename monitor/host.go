package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/yourusername/monitor-service/config"
)

// Host monitors host resources and returns alerts if thresholds are exceeded.
func Host(ctx context.Context, cfg config.HostConfig) ([]string, error) {
	msgs := []string{}

	// CPU usage
	cpuPercents, err := cpu.Percent(0, false)
	if err != nil {
		return []string{fmt.Sprintf("**Host**: Failed to get CPU usage: %v", err)}, err
	}
	cpuAvg := 0.0
	for _, p := range cpuPercents {
		cpuAvg += p
	}
	cpuAvg /= float64(len(cpuPercents))
	if cpuAvg > cfg.CPUThreshold {
		msgs = append(msgs, fmt.Sprintf("**Host**: High CPU usage: %.2f%% > %.2f%%", cpuAvg, cfg.CPUThreshold))
	}

	// Memory usage
	vm, err := mem.VirtualMemory()
	if err != nil {
		return []string{fmt.Sprintf("**Host**: Failed to get memory usage: %v", err)}, err
	}
	if vm.UsedPercent > cfg.MemThreshold {
		msgs = append(msgs, fmt.Sprintf("**Host**: High memory usage: %.2f%% > %.2f%%", vm.UsedPercent, cfg.MemThreshold))
	}

	// Disk usage (root)
	du, err := disk.Usage("/")
	if err != nil {
		return []string{fmt.Sprintf("**Host**: Failed to get disk usage: %v", err)}, err
	}
	if du.UsedPercent > cfg.DiskThreshold {
		msgs = append(msgs, fmt.Sprintf("**Host**: High disk usage: %.2f%% > %.2f%%", du.UsedPercent, cfg.DiskThreshold))
	}

	// Network IO rate
	netIO1, err := net.IOCounters(false)
	if err != nil {
		return []string{fmt.Sprintf("**Host**: Failed to get network IO: %v", err)}, err
	}
	time.Sleep(1 * time.Second)
	netIO2, err := net.IOCounters(false)
	if err != nil {
		return []string{fmt.Sprintf("**Host**: Failed to get network IO: %v", err)}, err
	}
	netBytesSent := int64(netIO2[0].BytesSent - netIO1[0].BytesSent)
	netBytesRecv := int64(netIO2[0].BytesRecv - netIO1[0].BytesRecv)
	netIORate := netBytesSent + netBytesRecv // bytes/s
	if netIORate > cfg.NetIOThreshold {
		msgs = append(msgs, fmt.Sprintf("**Host**: High network IO rate: %d B/s > %d B/s", netIORate, cfg.NetIOThreshold))
	}

	// Disk IO rate
	diskIO1, err := disk.IOCounters()
	if err != nil {
		return []string{fmt.Sprintf("**Host**: Failed to get disk IO: %v", err)}, err
	}
	time.Sleep(1 * time.Second)
	diskIO2, err := disk.IOCounters()
	if err != nil {
		return []string{fmt.Sprintf("**Host**: Failed to get disk IO: %v", err)}, err
	}
	var diskRead, diskWrite int64
	for name, io1 := range diskIO1 {
		if io2, ok := diskIO2[name]; ok {
			diskRead += int64(io2.ReadBytes - io1.ReadBytes)
			diskWrite += int64(io2.WriteBytes - io1.WriteBytes)
		}
	}
	diskIORate := diskRead + diskWrite // bytes/s
	if diskIORate > cfg.DiskIOThreshold {
		msgs = append(msgs, fmt.Sprintf("**Host**: High disk IO rate: %d B/s > %d B/s", diskIORate, cfg.DiskIOThreshold))
	}

	if len(msgs) > 0 {
		return msgs, fmt.Errorf("host issues")
	}
	return nil, nil
}