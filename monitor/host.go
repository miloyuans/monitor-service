package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"monitor-service/config"
)

// Host monitors host resources and returns alerts if thresholds are exceeded.
func Host(ctx context.Context, cfg config.HostConfig, clusterName string) ([]string, error) {
	hasIssue := false
	statusLines := []string{
		fmt.Sprintf("服务环境: %s", clusterName),
	}

	// Hostname
	hostname, err := os.Hostname()
	if err != nil {
		slog.Warn("Failed to get hostname", "error", err)
		hostname = "unknown"
	}
	statusLines = append(statusLines, fmt.Sprintf("主机名: %s", hostname))

	// CPU usage
	cpuPercents, err := cpu.Percent(0, false)
	if err != nil {
		slog.Error("Failed to get CPU usage", "error", err)
		return []string{fmt.Sprintf("**Host (%s)**: Failed to get CPU usage: %v", clusterName, err)}, err
	}
	cpuAvg := 0.0
	for _, p := range cpuPercents {
		cpuAvg += p
	}
	cpuAvg /= float64(len(cpuPercents))
	cpuStatus := "正常✅"
	if cpuAvg > cfg.CPUThreshold {
		cpuStatus = fmt.Sprintf("异常❌ %.2f%% > %.2f%%", cpuAvg, cfg.CPUThreshold)
		hasIssue = true
	}
	statusLines = append(statusLines, fmt.Sprintf("CPU使用率: %s", cpuStatus))

	// Memory usage (remaining rate)
	vm, err := mem.VirtualMemory()
	if err != nil {
		slog.Error("Failed to get memory usage", "error", err)
		return []string{fmt.Sprintf("**Host (%s)**: Failed to get memory usage: %v", clusterName, err)}, err
	}
	remainingPercent := 100.0 - vm.UsedPercent
	remainingThreshold := 100.0 - cfg.MemThreshold
	memStatus := "正常✅"
	if remainingPercent < remainingThreshold {
		memStatus = fmt.Sprintf("异常❌ %.2f%% < %.2f%%", remainingPercent, remainingThreshold)
		hasIssue = true
	}
	statusLines = append(statusLines, fmt.Sprintf("内存剩余率: %s", memStatus))

	// Network IO rate (in GB/s)
	const bytesToGB = 1.0 / (1024 * 1024 * 1024) // 1 GB = 10^9 bytes
	netIO1, err := net.IOCounters(false)
	if err != nil {
		slog.Error("Failed to get network IO", "error", err)
		return []string{fmt.Sprintf("**Host (%s)**: Failed to get network IO: %v", clusterName, err)}, err
	}
	time.Sleep(1 * time.Second)
	netIO2, err := net.IOCounters(false)
	if err != nil {
		slog.Error("Failed to get network IO", "error", err)
		return []string{fmt.Sprintf("**Host (%s)**: Failed to get network IO: %v", clusterName, err)}, err
	}
	netBytesSent := float64(netIO2[0].BytesSent - netIO1[0].BytesSent) * bytesToGB
	netBytesRecv := float64(netIO2[0].BytesRecv - netIO1[0].BytesRecv) * bytesToGB
	netIORate := netBytesSent + netBytesRecv // GB/s
	netIOStatus := "正常✅"
	if netIORate > cfg.NetIOThreshold {
		netIOStatus = fmt.Sprintf("异常❌ %.4f GB/s > %.4f GB/s", netIORate, cfg.NetIOThreshold)
		hasIssue = true
	}
	statusLines = append(statusLines, fmt.Sprintf("网络IO使用率: %s", netIOStatus))

	// Disk IO rate (in GB/s)
	diskIO1, err := disk.IOCounters()
	if err != nil {
		slog.Error("Failed to get disk IO", "error", err)
		return []string{fmt.Sprintf("**Host (%s)**: Failed to get disk IO: %v", clusterName, err)}, err
	}
	time.Sleep(1 * time.Second)
	diskIO2, err := disk.IOCounters()
	if err != nil {
		slog.Error("Failed to get disk IO", "error", err)
		return []string{fmt.Sprintf("**Host (%s)**: Failed to get disk IO: %v", clusterName, err)}, err
	}
	var diskRead, diskWrite float64
	for name, io1 := range diskIO1 {
		if io2, ok := diskIO2[name]; ok {
			diskRead += float64(io2.ReadBytes - io1.ReadBytes) * bytesToGB
			diskWrite += float64(io2.WriteBytes - io1.WriteBytes) * bytesToGB
		}
	}
	diskIORate := diskRead + diskWrite // GB/s
	diskIOStatus := "正常✅"
	if diskIORate > cfg.DiskIOThreshold {
		diskIOStatus = fmt.Sprintf("异常❌ %.4f GB/s > %.4f GB/s", diskIORate, cfg.DiskIOThreshold)
		hasIssue = true
	}
	statusLines = append(statusLines, fmt.Sprintf("磁盘IO使用率: %s", diskIOStatus))

	// Disk usage (root)
	du, err := disk.Usage("/")
	if err != nil {
		slog.Error("Failed to get disk usage", "error", err)
		return []string{fmt.Sprintf("**Host (%s)**: Failed to get disk usage: %v", clusterName, err)}, err
	}
	diskStatus := "正常✅"
	if du.UsedPercent > cfg.DiskThreshold {
		diskStatus = fmt.Sprintf("异常❌ %.2f%% > %.2f%%", du.UsedPercent, cfg.DiskThreshold)
		hasIssue = true
	}
	statusLines = append(statusLines, fmt.Sprintf("磁盘使用率: %s", diskStatus))

	if hasIssue {
		return []string{strings.Join(statusLines, "\n")}, fmt.Errorf("host issues")
	}
	return nil, nil
}