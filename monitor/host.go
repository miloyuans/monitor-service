package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// Host monitors host resources and sends alerts if thresholds are exceeded.
func Host(ctx context.Context, cfg config.HostConfig, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration) error {
	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "host")
		hostIP = "unknown"
	}

	// Initialize details for alert message
	var details strings.Builder
	hasIssue := false

	// Get processes once for both CPU and memory to reduce system calls
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		slog.Error("Failed to get processes", "error", err, "component", "host")
		details.WriteString(fmt.Sprintf("无法获取进程列表: %v", err))
		msg := bot.FormatAlert("主机告警", "服务异常", details.String(), hostIP, "alert")
		return sendHostAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "主机告警", "服务异常", details.String(), hostIP, "alert", "", map[string]interface{}{})
	}

	// CPU usage
	cpuPercents, err := cpu.PercentWithContext(ctx, time.Second, false)
	if err != nil {
		slog.Error("Failed to get CPU usage", "error", err, "component", "host")
		details.WriteString(fmt.Sprintf("无法获取 CPU 使用率: %v", err))
		msg := bot.FormatAlert("主机告警", "服务异常", details.String(), hostIP, "alert")
		return sendHostAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "主机告警", "服务异常", details.String(), hostIP, "alert", "", map[string]interface{}{})
	}
	var cpuAvg float64
	for _, p := range cpuPercents {
		if p >= 0 {
			cpuAvg += p
		}
	}
	if len(cpuPercents) > 0 {
		cpuAvg /= float64(len(cpuPercents))
	}
	cpuStatus := "正常✅"
	cpuTopProcsMsg := ""
	if cpuAvg > cfg.CPUThreshold {
		cpuStatus = fmt.Sprintf("异常❌ %.2f%% > %.2f%%", cpuAvg, cfg.CPUThreshold)
		hasIssue = true
		if cpuTopProcsMsg, err = getTopCPUProcesses(ctx, procs, 3); err != nil {
			slog.Warn("Failed to get top CPU processes", "error", err, "component", "host")
		}
	}
	fmt.Fprintf(&details, "**CPU 使用率**: %s\n", cpuStatus)
	if cpuTopProcsMsg != "" {
		details.WriteString(cpuTopProcsMsg)
	}

	// Memory usage (remaining rate)
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		slog.Error("Failed to get memory usage", "error", err, "component", "host")
		details.WriteString(fmt.Sprintf("无法获取内存使用率: %v", err))
		msg := bot.FormatAlert("主机告警", "服务异常", details.String(), hostIP, "alert")
		return sendHostAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "主机告警", "服务异常", details.String(), hostIP, "alert", "", map[string]interface{}{})
	}
	remainingPercent := 100.0 - vm.UsedPercent
	remainingThreshold := 100.0 - cfg.MemThreshold
	memStatus := "正常✅"
	memTopProcsMsg := ""
	if remainingPercent < remainingThreshold {
		memStatus = fmt.Sprintf("异常❌ %.2f%% < %.2f%%", remainingPercent, remainingThreshold)
		hasIssue = true
		if memTopProcsMsg, err = getTopMemoryProcesses(ctx, procs, 3); err != nil {
			slog.Warn("Failed to get top memory processes", "error", err, "component", "host")
		}
	}
	fmt.Fprintf(&details, "**内存剩余率**: %s\n", memStatus)
	if memTopProcsMsg != "" {
		details.WriteString(memTopProcsMsg)
	}

	// Network IO rate (in GB/s)
	const bytesToGB = 1.0 / (1024 * 1024 * 1024) // 1 GB = 10^9 bytes
	netIO1, err := net.IOCountersWithContext(ctx, false)
	if err != nil {
		slog.Error("Failed to get initial network IO", "error", err, "component", "host")
		details.WriteString(fmt.Sprintf("无法获取网络 IO: %v", err))
		msg := bot.FormatAlert("主机告警", "服务异常", details.String(), hostIP, "alert")
		return sendHostAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "主机告警", "服务异常", details.String(), hostIP, "alert", "", map[string]interface{}{})
	}
	select {
	case <-time.After(time.Second):
	case <-ctx.Done():
		slog.Warn("Network IO measurement cancelled", "component", "host")
		return ctx.Err()
	}
	netIO2, err := net.IOCountersWithContext(ctx, false)
	if err != nil {
		slog.Error("Failed to get final network IO", "error", err, "component", "host")
		details.WriteString(fmt.Sprintf("无法获取网络 IO: %v", err))
		msg := bot.FormatAlert("主机告警", "服务异常", details.String(), hostIP, "alert")
		return sendHostAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "主机告警", "服务异常", details.String(), hostIP, "alert", "", map[string]interface{}{})
	}
	var netBytesSent, netBytesRecv float64
	for i, io1 := range netIO1 {
		if i < len(netIO2) {
			sent := float64(netIO2[i].BytesSent-io1.BytesSent) * bytesToGB
			recv := float64(netIO2[i].BytesRecv-io1.BytesRecv) * bytesToGB
			if sent >= 0 {
				netBytesSent += sent
			}
			if recv >= 0 {
				netBytesRecv += recv
			}
		}
	}
	netIORate := netBytesSent + netBytesRecv // GB/s
	netIOStatus := "正常✅"
	if netIORate > cfg.NetIOThreshold {
		netIOStatus = fmt.Sprintf("异常❌ %.4f GB/s > %.4f GB/s", netIORate, cfg.NetIOThreshold)
		hasIssue = true
	}
	fmt.Fprintf(&details, "**网络 IO 使用率**: %s\n", netIOStatus)

	// Disk IO rate (in GB/s)
	diskIO1, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		slog.Error("Failed to get initial disk IO", "error", err, "component", "host")
		details.WriteString(fmt.Sprintf("无法获取磁盘 IO: %v", err))
		msg := bot.FormatAlert("主机告警", "服务异常", details.String(), hostIP, "alert")
		return sendHostAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "主机告警", "服务异常", details.String(), hostIP, "alert", "", map[string]interface{}{})
	}
	select {
	case <-time.After(time.Second):
	case <-ctx.Done():
		slog.Warn("Disk IO measurement cancelled", "component", "host")
		return ctx.Err()
	}
	diskIO2, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		slog.Error("Failed to get final disk IO", "error", err, "component", "host")
		details.WriteString(fmt.Sprintf("无法获取磁盘 IO: %v", err))
		msg := bot.FormatAlert("主机告警", "服务异常", details.String(), hostIP, "alert")
		return sendHostAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "主机告警", "服务异常", details.String(), hostIP, "alert", "", map[string]interface{}{})
	}
	var diskRead, diskWrite float64
	for name, io1 := range diskIO1 {
		if io2, ok := diskIO2[name]; ok {
			read := float64(io2.ReadBytes-io1.ReadBytes) * bytesToGB
			write := float64(io2.WriteBytes-io1.WriteBytes) * bytesToGB
			if read >= 0 {
				diskRead += read
			}
			if write >= 0 {
				diskWrite += write
			}
		}
	}
	diskIORate := diskRead + diskWrite // GB/s
	diskIOStatus := "正常✅"
	if diskIORate > cfg.DiskIOThreshold {
		diskIOStatus = fmt.Sprintf("异常❌ %.4f GB/s > %.4f GB/s", diskIORate, cfg.DiskIOThreshold)
		hasIssue = true
	}
	fmt.Fprintf(&details, "**磁盘 IO 使用率**: %s\n", diskIOStatus)

	// Disk usage (root)
	du, err := disk.UsageWithContext(ctx, "/")
	if err != nil {
		slog.Error("Failed to get disk usage", "path", "/", "error", err, "component", "host")
		details.WriteString(fmt.Sprintf("无法获取磁盘使用率: %v", err))
		msg := bot.FormatAlert("主机告警", "服务异常", details.String(), hostIP, "alert")
		return sendHostAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "主机告警", "服务异常", details.String(), hostIP, "alert", "", map[string]interface{}{})
	}
	diskStatus := "正常✅"
	diskTopDirsMsg := ""
	if du.UsedPercent > cfg.DiskThreshold {
		diskStatus = fmt.Sprintf("异常❌ %.2f%% > %.2f%%", du.UsedPercent, cfg.DiskThreshold)
		hasIssue = true
		if diskTopDirsMsg, err = getTopDiskDirectories(ctx, 3); err != nil {
			slog.Warn("Failed to get top disk directories", "error", err, "component", "host")
		}
	}
	fmt.Fprintf(&details, "**磁盘使用率**: %s\n", diskStatus)
	if diskTopDirsMsg != "" {
		details.WriteString(diskTopDirsMsg)
	}

	if hasIssue {
		slog.Info("Host resource issues detected", "cpu", cpuStatus, "memory", memStatus, "net_io", netIOStatus, "disk_io", diskIOStatus, "disk_io_rate", diskIORate, "disk", diskStatus, "component", "host")
		msg := bot.FormatAlert("主机告警", "服务异常", details.String(), hostIP, "alert")
		return sendHostAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "主机告警", "服务异常", details.String(), hostIP, "alert", "", map[string]interface{}{})
	}
	slog.Debug("No host resource issues detected", "component", "host")
	return nil
}

// sendHostAlert sends a deduplicated Telegram alert for the Host module.
// extra: additional alert message (optional).
// metadata: additional key-value data for the alert (optional).
func sendHostAlert(ctx context.Context, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, serviceName, eventName, details, hostIP, alertType, extra string, metadata map[string]interface{}) error {
	hash, err := util.MD5Hash(details)
	if err != nil {
		slog.Error("Failed to generate alert hash", "error", err, "component", "host")
		return fmt.Errorf("failed to generate alert hash: %w", err)
	}
	cacheMutex.Lock()
	now := time.Now()
	if timestamp, ok := alertCache[hash]; ok && now.Sub(timestamp) < alertSilenceDuration {
		slog.Info("Skipping duplicate alert", "hash", hash, "component", "host")
		cacheMutex.Unlock()
		return nil
	}
	alertCache[hash] = now
	// Clean up old cache entries
	for h, t := range alertCache {
		if now.Sub(t) >= alertSilenceDuration {
			delete(alertCache, h)
			slog.Debug("Removed expired alert cache entry", "hash", h, "component", "host")
		}
	}
	cacheMutex.Unlock()
	slog.Debug("Sending alert", "service", serviceName, "event", eventName, "details", details, "component", "host")
	if err := bot.SendAlert(ctx, serviceName, eventName, details, hostIP, alertType, extra, metadata); err != nil {
		slog.Error("Failed to send alert", "error", err, "service", serviceName, "event", eventName, "details", details, "component", "host")
		return fmt.Errorf("failed to send alert: %w", err)
	}
	slog.Info("Sent alert", "service", serviceName, "event", eventName, "details", details, "component", "host")
	return nil
}

// getTopCPUProcesses gets the top N processes by CPU usage from the provided process list.
func getTopCPUProcesses(ctx context.Context, procs []*process.Process, n int) (string, error) {
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
		if cpu, err := p.CPUPercentWithContext(ctx); err == nil && cpu > 0 {
			name, err := p.NameWithContext(ctx)
			if err != nil {
				name = "unknown"
				slog.Warn("Failed to get process name", "pid", p.Pid, "error", err, "component", "host")
			}
			user, err := p.UsernameWithContext(ctx)
			if err != nil {
				user = "?"
				slog.Warn("Failed to get process username", "pid", p.Pid, "error", err, "component", "host")
			}
			createTime, err := p.CreateTimeWithContext(ctx)
			if err != nil {
				createTime = 0
				slog.Warn("Failed to get process create time", "pid", p.Pid, "error", err, "component", "host")
			}
			stime := time.UnixMilli(createTime).Format("Jan 02 15:04")
			tty, err := p.TerminalWithContext(ctx)
			if err != nil {
				tty = "?"
				slog.Warn("Failed to get process terminal", "pid", p.Pid, "error", err, "component", "host")
			}
			top = append(top, procCPU{user: user, pid: p.Pid, name: name, cpu: cpu, stime: stime, tty: tty})
		}
	}
	if len(top) == 0 {
		slog.Debug("No processes with CPU usage found", "component", "host")
		return "", nil
	}
	sort.Slice(top, func(i, j int) bool { return top[i].cpu > top[j].cpu })
	var msg strings.Builder
	msg.WriteString("\n**最消耗 CPU 的 3 个进程**:\n")
	fmt.Fprintf(&msg, "| %s | %s | %s | %s | %s | %s |\n",
		"User", "PID", "Name", "CPU%", "Start Time", "TTY")
	fmt.Fprintf(&msg, "|%s|%s|%s|%s|%s|%s|\n",
		"---", "---", "---", "---", "---", "---")
	for i := 0; i < n && i < len(top); i++ {
		fmt.Fprintf(&msg, "| %s | %d | %s | %.2f | %s | %s |\n",
			top[i].user,
			top[i].pid,
			top[i].name,
			top[i].cpu,
			top[i].stime,
			top[i].tty)
	}
	return msg.String(), nil
}

// getTopMemoryProcesses gets the top N processes by memory usage from the provided process list.
func getTopMemoryProcesses(ctx context.Context, procs []*process.Process, n int) (string, error) {
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
		if mem, err := p.MemoryInfoWithContext(ctx); err == nil && mem.RSS > 0 {
			name, err := p.NameWithContext(ctx)
			if err != nil {
				name = "unknown"
				slog.Warn("Failed to get process name", "pid", p.Pid, "error", err, "component", "host")
			}
			user, err := p.UsernameWithContext(ctx)
			if err != nil {
				user = "?"
				slog.Warn("Failed to get process username", "pid", p.Pid, "error", err, "component", "host")
			}
			createTime, err := p.CreateTimeWithContext(ctx)
			if err != nil {
				createTime = 0
				slog.Warn("Failed to get process create time", "pid", p.Pid, "error", err, "component", "host")
			}
			stime := time.UnixMilli(createTime).Format("Jan 02 15:04")
			tty, err := p.TerminalWithContext(ctx)
			if err != nil {
				tty = "?"
				slog.Warn("Failed to get process terminal", "pid", p.Pid, "error", err, "component", "host")
			}
			top = append(top, procMem{user: user, pid: p.Pid, name: name, mem: mem.RSS, stime: stime, tty: tty})
		}
	}
	if len(top) == 0 {
		slog.Debug("No processes with memory usage found", "component", "host")
		return "", nil
	}
	sort.Slice(top, func(i, j int) bool { return top[i].mem > top[j].mem })
	const bytesToMB = 1.0 / (1024 * 1024) // Convert bytes to MB
	var msg strings.Builder
	msg.WriteString("\n**最消耗内存的 3 个进程**:\n")
	fmt.Fprintf(&msg, "| %s | %s | %s | %s | %s | %s |\n",
		"User", "PID", "Name", "Memory (MB)", "Start Time", "TTY")
	fmt.Fprintf(&msg, "|%s|%s|%s|%s|%s|%s|\n",
		"---", "---", "---", "---", "---", "---")
	for i := 0; i < n && i < len(top); i++ {
		memMB := float64(top[i].mem) * bytesToMB
		fmt.Fprintf(&msg, "| %s | %d | %s | %.2f | %s | %s |\n",
			top[i].user,
			top[i].pid,
			top[i].name,
			memMB,
			top[i].stime,
			top[i].tty)
	}
	return msg.String(), nil
}

// getTopDiskDirectories gets the top N directories by disk usage.
func getTopDiskDirectories(ctx context.Context, n int) (string, error) {
	// Use platform-specific command
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		slog.Warn("getTopDiskDirectories not implemented for Windows", "component", "host")
		return "", fmt.Errorf("disk usage monitoring not supported on Windows")
	} else {
		cmd = exec.CommandContext(ctx, "du", "-sh", "/*")
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("Failed to get disk usage for directories", "error", err, "output", string(output), "component", "host")
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
		slog.Debug("No directories with valid disk usage found", "component", "host")
		return "", nil
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].size > dirs[j].size })
	var msg strings.Builder
	msg.WriteString("\n**最占用磁盘空间的 3 个目录**:\n")
	fmt.Fprintf(&msg, "| %s | %s |\n",
		"Size", "Path")
	fmt.Fprintf(&msg, "|%s|%s|\n",
		"---", "---")
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
	unit := byte(0)
	if len(size) > 0 && (size[len(size)-1] < '0' || size[len(size)-1] > '9') {
		unit = size[len(size)-1]
		size = size[:len(size)-1]
	}
	value, err := strconv.ParseFloat(size, 64)
	if err != nil {
		slog.Warn("Failed to parse size", "size", size, "error", err, "component", "host")
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