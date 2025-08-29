package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
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
	cpuTopProcsMsg := ""
	if cpuAvg > cfg.CPUThreshold {
		cpuStatus = fmt.Sprintf("异常❌ %.2f%% > %.2f%%", cpuAvg, cfg.CPUThreshold)
		hasIssue = true
		cpuTopProcsMsg, _ = getTopCPUProcesses(3)
	}
	statusLines = append(statusLines, fmt.Sprintf("CPU使用率: %s", cpuStatus))
	if cpuTopProcsMsg != "" {
		statusLines = append(statusLines, cpuTopProcsMsg)
	}

	// Memory usage (remaining rate)
	vm, err := mem.VirtualMemory()
	if err != nil {
		slog.Error("Failed to get memory usage", "error", err)
		return []string{fmt.Sprintf("**Host (%s)**: Failed to get memory usage: %v", clusterName, err)}, err
	}
	remainingPercent := 100.0 - vm.UsedPercent
	remainingThreshold := 100.0 - cfg.MemThreshold
	memStatus := "正常✅"
	memTopProcsMsg := ""
	if remainingPercent < remainingThreshold {
		memStatus = fmt.Sprintf("异常❌ %.2f%% < %.2f%%", remainingPercent, remainingThreshold)
		hasIssue = true
		memTopProcsMsg, _ = getTopMemoryProcesses(3)
	}
	statusLines = append(statusLines, fmt.Sprintf("内存剩余率: %s", memStatus))
	if memTopProcsMsg != "" {
		statusLines = append(statusLines, memTopProcsMsg)
	}

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
	diskTopDirsMsg := ""
	if du.UsedPercent > cfg.DiskThreshold {
		diskStatus = fmt.Sprintf("异常❌ %.2f%% > %.2f%%", du.UsedPercent, cfg.DiskThreshold)
		hasIssue = true
		diskTopDirsMsg, _ = getTopDiskDirectories(3)
	}
	statusLines = append(statusLines, fmt.Sprintf("磁盘使用率: %s", diskStatus))
	if diskTopDirsMsg != "" {
		statusLines = append(statusLines, diskTopDirsMsg)
	}

	if hasIssue {
		return []string{strings.Join(statusLines, "\n")}, fmt.Errorf("host issues")
	}
	return nil, nil
}

// getTopCPUProcesses gets the top N processes by CPU usage.
func getTopCPUProcesses(n int) (string, error) {
	procs, err := process.Processes()
	if err != nil {
		return "", err
	}
	type procCPU struct {
		pid   int32
		name  string
		cpu   float64
	}
	var top []procCPU
	for _, p := range procs {
		cpu, err := p.CPUPercent()
		if err == nil && cpu > 0 {
			name, _ := p.Name()
			top = append(top, procCPU{pid: p.Pid, name: name, cpu: cpu})
		}
	}
	sort.Slice(top, func(i, j int) bool { return top[i].cpu > top[j].cpu })
	msg := "**最消耗CPU的3个进程:**\n| PID | Name | CPU% |\n|-----|------|------|\n"
	for i := 0; i < n && i < len(top); i++ {
		msg += fmt.Sprintf("| %d | %s | %.2f |\n", top[i].pid, top[i].name, top[i].cpu)
	}
	return msg, nil
}

// getTopMemoryProcesses gets the top N processes by memory usage.
func getTopMemoryProcesses(n int) (string, error) {
	procs, err := process.Processes()
	if err != nil {
		return "", err
	}
	type procMem struct {
		pid   int32
		name  string
		mem   uint64
	}
	var top []procMem
	for _, p := range procs {
		mem, err := p.MemoryInfo()
		if err == nil && mem.RSS > 0 {
			name, _ := p.Name()
			top = append(top, procMem{pid: p.Pid, name: name, mem: mem.RSS})
		}
	}
	sort.Slice(top, func(i, j int) bool { return top[i].mem > top[j].mem })
	msg := "**最消耗内存的3个进程:**\n| PID | Name | Memory (bytes) |\n|-----|------|----------------|\n"
	for i := 0; i < n && i < len(top); i++ {
		msg += fmt.Sprintf("| %d | %s | %d |\n", top[i].pid, top[i].name, top[i].mem)
	}
	return msg, nil
}

// getTopDiskDirectories gets the top N directories by disk usage.
func getTopDiskDirectories(n int) (string, error) {
	cmd := exec.Command("du", "-sh", "/*")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(output), "\n")
	var dirs []struct {
		size string
		path string
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 {
			dirs = append(dirs, struct{ size, path string }{fields[0], fields[1]})
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		return parseSize(dirs[i].size) > parseSize(dirs[j].size)
	})
	msg := "**最占用磁盘空间的3个目录:**\n| Size | Path |\n|------|------|\n"
	for i := 0; i < n && i < len(dirs); i++ {
		msg += fmt.Sprintf("| %s | %s |\n", dirs[i].size, dirs[i].path)
	}
	return msg, nil
}

// parseSize parses size strings like "1.0K", "2.5M" to bytes for sorting.
func parseSize(size string) float64 {
	unit := size[len(size)-1]
	value, _ := strconv.ParseFloat(size[:len(size)-1], 64)
	switch unit {
	case 'K':
		return value * 1024
	case 'M':
		return value * 1024 * 1024
	case 'G':
		return value * 1024 * 1024 * 1024
	default:
		return value
	}
}