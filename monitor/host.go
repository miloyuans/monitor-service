package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/util"
)

// Host monitors host resources and returns alerts if thresholds are exceeded.
func Host(ctx context.Context, cfg config.HostConfig, alertBot *alert.AlertBot) ([]string, string, error) {
	hasIssue := false
	clusterPrefix := fmt.Sprintf("**Host (%s)**", alertBot.ClusterName)
	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err)
		hostIP = "unknown"
	}
	// Initialize alert message
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	statusLines := []string{
		"🚨 *监控 Monitoring 告警 Alert* 🚨",
		fmt.Sprintf("*时间*: %s", timestamp),
		fmt.Sprintf("*环境*: %s", alertBot.ClusterName),
	}
	// Hostname
	hostname := alertBot.Hostname
	if !alertBot.ShowHostname {
		hostname = "N/A"
	}
	statusLines = append(statusLines, fmt.Sprintf("*主机名*: %s", hostname))
	statusLines = append(statusLines, fmt.Sprintf("*主机IP*: %s", hostIP))
	statusLines = append(statusLines, fmt.Sprintf("*服务名*: Host (%s)", alertBot.ClusterName))
	statusLines = append(statusLines, "*事件名*: 服务异常")
	statusLines = append(statusLines, "*详情*:")

	// Get processes once for both CPU and memory to reduce system calls
	procs, err := process.Processes()
	if err != nil {
		slog.Error("Failed to get processes", "error", err, "component", "host_monitor")
		return []string{fmt.Sprintf("%s: Failed to get processes: %v", clusterPrefix, err)}, hostIP, err
	}

	// CPU usage
	cpuPercents, err := cpu.Percent(time.Second, false)
	if err != nil {
		slog.Error("Failed to get CPU usage", "error", err, "component", "host_monitor")
		return []string{fmt.Sprintf("%s: Failed to get CPU usage: %v", clusterPrefix, err)}, hostIP, err
	}
	cpuAvg := 0.0
	for _, p := range cpuPercents {
		cpuAvg += p
	}
	if len(cpuPercents) > 0 {
		cpuAvg /= float64(len(cpuPercents))
	}
	cpuStatus := "正常✅"
	cpuTopProcsMsg := ""
	if cpuAvg > cfg.CPUThreshold {
		cpuStatus = fmt.Sprintf("异常❌ %.2f%% > %.2f%%", cpuAvg, cfg.CPUThreshold)
		hasIssue = true
		if cpuTopProcsMsg, err = getTopCPUProcesses(procs, 3); err != nil {
			slog.Warn("Failed to get top CPU processes", "error", err, "component", "host_monitor")
		}
	}
	statusLines = append(statusLines, fmt.Sprintf("**CPU使用率**: %s", cpuStatus))
	if cpuTopProcsMsg != "" {
		statusLines = append(statusLines, cpuTopProcsMsg)
	}

	// Memory usage (remaining rate)
	vm, err := mem.VirtualMemory()
	if err != nil {
		slog.Error("Failed to get memory usage", "error", err, "component", "host_monitor")
		return []string{fmt.Sprintf("%s: Failed to get memory usage: %v", clusterPrefix, err)}, hostIP, err
	}
	remainingPercent := 100.0 - vm.UsedPercent
	remainingThreshold := 100.0 - cfg.MemThreshold
	memStatus := "正常✅"
	memTopProcsMsg := ""
	if remainingPercent < remainingThreshold {
		memStatus = fmt.Sprintf("异常❌ %.2f%% < %.2f%%", remainingPercent, remainingThreshold)
		hasIssue = true
		if memTopProcsMsg, err = getTopMemoryProcesses(procs, 3); err != nil {
			slog.Warn("Failed to get top memory processes", "error", err, "component", "host_monitor")
		}
	}
	statusLines = append(statusLines, fmt.Sprintf("**内存剩余率**: %s", memStatus))
	if memTopProcsMsg != "" {
		statusLines = append(statusLines, memTopProcsMsg)
	}

	// Network IO rate (in GB/s)
	const bytesToGB = 1.0 / (1024 * 1024 * 1024) // 1 GB = 10^9 bytes
	netIO1, err := net.IOCounters(false)
	if err != nil {
		slog.Error("Failed to get network IO", "error", err, "component", "host_monitor")
		return []string{fmt.Sprintf("%s: Failed to get network IO: %v", clusterPrefix, err)}, hostIP, err
	}
	time.Sleep(time.Second)
	netIO2, err := net.IOCounters(false)
	if err != nil {
		slog.Error("Failed to get network IO", "error", err, "component", "host_monitor")
		return []string{fmt.Sprintf("%s: Failed to get network IO: %v", clusterPrefix, err)}, hostIP, err
	}
	var netBytesSent, netBytesRecv float64
	for i, io1 := range netIO1 {
		if i < len(netIO2) {
			sent := float64(netIO2[i].BytesSent - io1.BytesSent)
			recv := float64(netIO2[i].BytesRecv - io1.BytesRecv)
			if sent >= 0 {
				netBytesSent += sent * bytesToGB
			}
			if recv >= 0 {
				netBytesRecv += recv * bytesToGB
			}
		}
	}
	netIORate := netBytesSent + netBytesRecv // GB/s
	netIOStatus := "正常✅"
	if netIORate > cfg.NetIOThreshold {
		netIOStatus = fmt.Sprintf("异常❌ %.4f GB/s > %.4f GB/s", netIORate, cfg.NetIOThreshold)
		hasIssue = true
	}
	statusLines = append(statusLines, fmt.Sprintf("**网络IO使用率**: %s", netIOStatus))

	// Disk IO rate (in GB/s)
	diskIO1, err := disk.IOCounters()
	if err != nil {
		slog.Error("Failed to get disk IO", "error", err, "component", "host_monitor")
		return []string{fmt.Sprintf("%s: Failed to get disk IO: %v", clusterPrefix, err)}, hostIP, err
	}
	time.Sleep(time.Second)
	diskIO2, err := disk.IOCounters()
	if err != nil {
		slog.Error("Failed to get disk IO", "error", err, "component", "host_monitor")
		return []string{fmt.Sprintf("%s: Failed to get disk IO: %v", clusterPrefix, err)}, hostIP, err
	}
	var diskRead, diskWrite float64
	for name, io1 := range diskIO1 {
		if io2, ok := diskIO2[name]; ok {
			read := float64(io2.ReadBytes - io1.ReadBytes)
			write := float64(io2.WriteBytes - io1.WriteBytes)
			if read >= 0 {
				diskRead += read * bytesToGB
			}
			if write >= 0 {
				diskWrite += write * bytesToGB
			}
		}
	}
	diskIORate := diskRead + diskWrite // GB/s
	diskIOStatus := "正常✅"
	if diskIORate > cfg.DiskIOThreshold {
		diskIOStatus = fmt.Sprintf("异常❌ %.4f GB/s > %.4f GB/s", diskIORate, cfg.DiskIOThreshold)
		hasIssue = true
	}
	statusLines = append(statusLines, fmt.Sprintf("**磁盘IO使用率**: %s", diskIOStatus))

	// Disk usage (root)
	du, err := disk.Usage("/")
	if err != nil {
		slog.Error("Failed to get disk usage", "error", err, "component", "host_monitor")
		return []string{fmt.Sprintf("%s: Failed to get disk usage: %v", clusterPrefix, err)}, hostIP, err
	}
	diskStatus := "正常✅"
	diskTopDirsMsg := ""
	if du.UsedPercent > cfg.DiskThreshold {
		diskStatus = fmt.Sprintf("异常❌ %.2f%% > %.2f%%", du.UsedPercent, cfg.DiskThreshold)
		hasIssue = true
		if diskTopDirsMsg, err = getTopDiskDirectories(ctx, 3); err != nil {
			slog.Warn("Failed to get top disk directories", "error", err, "component", "host_monitor")
		}
	}
	statusLines = append(statusLines, fmt.Sprintf("**磁盘使用率**: %s", diskStatus))
	if diskTopDirsMsg != "" {
		statusLines = append(statusLines, diskTopDirsMsg)
	}

	if hasIssue {
		return []string{strings.Join(statusLines, "\n")}, hostIP, fmt.Errorf("host issues")
	}
	return nil, "", nil
}

// getTopCPUProcesses gets the top N processes by CPU usage from the provided process list.
func getTopCPUProcesses(procs []*process.Process, n int) (string, error) {
	type procCPU struct {
		user  string
		pid   int32
		name  string
		cpu   float64
		stime string
		tty   string
	}
	var top []procCPU
	for _, p := range procs {
		if cpu, err := p.CPUPercent(); err == nil && cpu > 0 {
			name, err := p.Name()
			if err != nil {
				name = "unknown"
			}
			user, err := p.Username()
			if err != nil {
				user = "?"
			}
			createTime, err := p.CreateTime()
			if err != nil {
				createTime = 0
			}
			stime := time.UnixMilli(createTime).Format("Jan 02 15:04")
			tty, err := p.Terminal()
			if err != nil {
				tty = "?"
			}
			top = append(top, procCPU{user: user, pid: p.Pid, name: name, cpu: cpu, stime: stime, tty: tty})
		}
	}
	if len(top) == 0 {
		return "", nil
	}
	sort.Slice(top, func(i, j int) bool { return top[i].cpu > top[j].cpu })
	var msg strings.Builder
	msg.WriteString("**最消耗CPU的3个进程:**\n| User | PID | Name | CPU% | Start Time | TTY |\n|------|-----|------|------|------------|-----|\n")
	for i := 0; i < n && i < len(top); i++ {
		fmt.Fprintf(&msg, "| %s | %d | %s | %.2f | %s | %s |\n", top[i].user, top[i].pid, top[i].name, top[i].cpu, top[i].stime, top[i].tty)
	}
	return msg.String(), nil
}

// getTopMemoryProcesses gets the top N processes by memory usage from the provided process list.
func getTopMemoryProcesses(procs []*process.Process, n int) (string, error) {
	type procMem struct {
		user  string
		pid   int32
		name  string
		mem   uint64
		stime string
		tty   string
	}
	var top []procMem
	for _, p := range procs {
		if mem, err := p.MemoryInfo(); err == nil && mem.RSS > 0 {
			name, err := p.Name()
			if err != nil {
				name = "unknown"
			}
			user, err := p.Username()
			if err != nil {
				user = "?"
			}
			createTime, err := p.CreateTime()
			if err != nil {
				createTime = 0
			}
			stime := time.UnixMilli(createTime).Format("Jan 02 15:04")
			tty, err := p.Terminal()
			if err != nil {
				tty = "?"
			}
			top = append(top, procMem{user: user, pid: p.Pid, name: name, mem: mem.RSS, stime: stime, tty: tty})
		}
	}
	if len(top) == 0 {
		return "", nil
	}
	sort.Slice(top, func(i, j int) bool { return top[i].mem > top[j].mem })
	const bytesToMB = 1.0 / (1024 * 1024) // Convert bytes to MB
	var msg strings.Builder
	msg.WriteString("**最消耗内存的3个进程:**\n| User | PID | Name | Memory (MB) | Start Time | TTY |\n|------|-----|------|-------------|------------|-----|\n")
	for i := 0; i < n && i < len(top); i++ {
		memMB := float64(top[i].mem) * bytesToMB
		fmt.Fprintf(&msg, "| %s | %d | %s | %.2f | %s | %s |\n", top[i].user, top[i].pid, top[i].name, memMB, top[i].stime, top[i].tty)
	}
	return msg.String(), nil
}

// getTopDiskDirectories gets the top N directories by disk usage.
func getTopDiskDirectories(ctx context.Context, n int) (string, error) {
	cmd := exec.CommandContext(ctx, "du", "-sh", "/*")
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("Failed to get disk usage for directories", "error", err, "output", string(output), "component", "host_monitor")
		return "", fmt.Errorf("du command failed: %w", err)
	}
	lines := strings.Split(string(output), "\n")
	var dirs []struct {
		size    float64
		sizeStr string
		path    string
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		size, sizeStr := parseSize(fields[0])
		if size == 0 {
			continue // Skip invalid sizes
		}
		dirs = append(dirs, struct{ size float64; sizeStr string; path string }{size: size, sizeStr: sizeStr, path: fields[1]})
	}
	if len(dirs) == 0 {
		return "", nil
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].size > dirs[j].size })
	var msg strings.Builder
	msg.WriteString("**最占用磁盘空间的3个目录:**\n| Size | Path |\n|------|------|\n")
	for i := 0; i < n && i < len(dirs); i++ {
		fmt.Fprintf(&msg, "| %s | %s |\n", dirs[i].sizeStr, dirs[i].path)
	}
	return msg.String(), nil
}

// parseSize parses size strings like "1.0K", "2.5M" to bytes for sorting and returns original string.
func parseSize(size string) (float64, string) {
	if len(size) == 0 {
		return 0, ""
	}
	// Handle cases like "123" (no unit) or "123.4K"
	unit := byte(0)
	if len(size) > 0 && (size[len(size)-1] < '0' || size[len(size)-1] > '9') {
		unit = size[len(size)-1]
		size = size[:len(size)-1]
	}
	value, err := strconv.ParseFloat(size, 64)
	if err != nil {
		slog.Warn("Failed to parse size", "size", size, "error", err, "component", "host_monitor")
		return 0, ""
	}
	originalSize := size
	if unit != 0 {
		originalSize += string(unit)
	}
	switch unit {
	case 'K':
		return value * 1024, originalSize
	case 'M':
		return value * 1024 * 1024, originalSize
	case 'G':
		return value * 1024 * 1024 * 1024, originalSize
	case 'T':
		return value * 1024 * 1024 * 1024 * 1024, originalSize
	default:
		return value, originalSize
	}
}