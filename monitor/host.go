package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"monitor-service/config"
)

// Host monitors host resources and returns alerts if thresholds are exceeded.
func Host(ctx context.Context, cfg config.HostConfig, clusterName string) ([]string, error) {
	msgs := []string{}
	clusterPrefix := fmt.Sprintf("**Host (%s)**", clusterName)

	// CPU usage
	cpuPercents, err := cpu.Percent(0, false)
	if err != nil {
		slog.Error("Failed to get CPU usage", "error", err)
		return []string{fmt.Sprintf("%s: Failed to get CPU usage: %v", clusterPrefix, err)}, err
	}
	cpuAvg := 0.0
	for _, p := range cpuPercents {
		cpuAvg += p
	}
	cpuAvg /= float64(len(cpuPercents))
	if cpuAvg > cfg.CPUThreshold {
		msgs = append(msgs, fmt.Sprintf("%s: High CPU usage: %.2f%% > %.2f%%", clusterPrefix, cpuAvg, cfg.CPUThreshold))
	}

	// Memory usage
	vm, err := mem.VirtualMemory()
	if err != nil {
		slog.Error("Failed to get memory usage", "error", err)
		return []string{fmt.Sprintf("%s: Failed to get memory usage: %v", clusterPrefix, err)}, err
	}
	if vm.UsedPercent > cfg.MemThreshold {
		msgs = append(msgs, fmt.Sprintf("%s: High memory usage: %.2f%% > %.2f%%", clusterPrefix, vm.UsedPercent, cfg.MemThreshold))
	}

	// Disk usage (root)
	du, err := disk.Usage("/")
	if err != nil {
		slog.Error("Failed to get disk usage", "error", err)
		return []string{fmt.Sprintf("%s: Failed to get disk usage: %v", clusterPrefix, err)}, err
	}
	if du.UsedPercent > cfg.DiskThreshold {
		msgs = append(msgs, fmt.Sprintf("%s: High disk usage: %.2f%% > %.2f%%", clusterPrefix, du.UsedPercent, cfg.DiskThreshold))
	}

	// Network IO rate (in GB/s)
	const bytesToGB = 1.0 / (1024 * 1024 * 1024) // 1 GB = 10^9 bytes
	netIO1, err := net.IOCounters(false)
	if err != nil {
		slog.Error("Failed to get network IO", "error", err)
		return []string{fmt.Sprintf("%s: Failed to get network IO: %v", clusterPrefix, err)}, err
	}
	time.Sleep(1 * time.Second)
	netIO2, err := net.IOCounters(false)
	if err != nil {
		slog.Error("Failed to get network IO", "error", err)
		return []string{fmt.Sprintf("%s: Failed to get network IO: %v", clusterPrefix, err)}, err
	}
	netBytesSent := float64(netIO2[0].BytesSent-netIO1[0].BytesSent) * bytesToGB
	netBytesRecv := float64(netIO2[0].BytesRecv-netIO1[0].BytesRecv) * bytesToGB
	netIORate := netBytesSent + netBytesRecv // GB/s
	if netIORate > cfg.NetIOThreshold {
		msgs = append(msgs, fmt.Sprintf("%s: High network IO rate: %.4f GB/s > %.4f GB/s", clusterPrefix, netIORate, cfg.NetIOThreshold))
	}

	// Disk IO rate (in GB/s)
	diskIO1, err := disk.IOCounters()
	if err != nil {
		slog.Error("Failed to get disk IO", "error", err)
		return []string{fmt.Sprintf("%s: Failed to get disk IO: %v", clusterPrefix, err)}, err
	}
	time.Sleep(1 * time.Second)
	diskIO2, err := disk.IOCounters()
	if err != nil {
		slog.Error("Failed to get disk IO", "error", err)
		return []string{fmt.Sprintf("%s: Failed to get disk IO: %v", clusterPrefix, err)}, err
	}
	var diskRead, diskWrite float64
	for name, io1 := range diskIO1 {
		if io2, ok := diskIO2[name]; ok {
			diskRead += float64(io2.ReadBytes-io1.ReadBytes) * bytesToGB
			diskWrite += float64(io2.WriteBytes-io1.WriteBytes) * bytesToGB
		}
	}
	diskIORate := diskRead + diskWrite // GB/s
	if diskIORate > cfg.DiskIOThreshold {
		msgs = append(msgs, fmt.Sprintf("%s: High disk IO rate: %.4f GB/s > %.4f GB/s", clusterPrefix, diskIORate, cfg.DiskIOThreshold))
	}

	if len(msgs) > 0 {
		return msgs, fmt.Errorf("host issues")
	}
	return nil, nil
}