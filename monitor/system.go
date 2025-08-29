package monitor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/process"
	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/util"
)

// UserInfo holds user information.
type UserInfo struct {
	Users []string `json:"users"`
}

// ProcessInfo holds process details.
type ProcessInfo struct {
	User  string `json:"user"`
	PID   int32  `json:"pid"`
	PPID  int32  `json:"ppid"`
	STIME string `json:"stime"`
	TTY   string `json:"tty"`
	TIME  string `json:"time"`
	CMD   string `json:"cmd"`
}

// ChangeLogEntry holds change details for logging.
type ChangeLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"` // "user" or "process"
	Added     []any     `json:"added"`
	Removed   []any     `json:"removed"`
}

// System monitors system users and processes for changes.
func System(ctx context.Context, cfg config.SystemConfig, alertBot *alert.AlertBot) ([]string, string, error) {
	msgs := []string{}
	serviceName := fmt.Sprintf("System (%s)", alertBot.ClusterName)

	// File size limit (500 MB)
	const maxFileSize = 500 * 1024 * 1024 // 500 MB in bytes
	// Change log file
	changeLogFile := ".changeLog.jsonl"
	// Files to check
	filesToCheck := []string{changeLogFile}

	// Check and cleanup historical files every 30 days
	lastCleanupFile := ".lastCleanup"
	if shouldCleanup(lastCleanupFile, 30*24*time.Hour) {
		if err := cleanupHistoricalFiles(15 * 24 * time.Hour); err != nil {
			slog.Error("Failed to cleanup historical files", "error", err, "component", "system_monitor")
			return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to cleanup historical files: %v", err), "", "alert")}, "", err
		}
		if err := updateLastCleanup(lastCleanupFile); err != nil {
			slog.Error("Failed to update last cleanup time", "error", err, "component", "system_monitor")
			return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to update last cleanup time: %v", err), "", "alert")}, "", err
		}
	}

	// Check file sizes and reinitialize if needed
	needsReinit := false
	for _, file := range filesToCheck {
		info, err := os.Stat(file)
		if err == nil && info.Size() > maxFileSize {
			needsReinit = true
			break
		}
	}

	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "system_monitor")
		hostIP = "unknown"
	}

	// Monitor users
	userInitialFile := ".userNumber"
	currentUsers, err := getCurrentUsers()
	if err != nil {
		slog.Error("Failed to get current users", "error", err, "component", "system_monitor")
		return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to get current users: %v", err), hostIP, "alert")}, hostIP, err
	}
	slog.Debug("Retrieved current users", "count", len(currentUsers), "component", "system_monitor")
	initialUsers, err := loadInitialUsers(userInitialFile)
	if err != nil {
		slog.Error("Failed to load initial users", "error", err, "component", "system_monitor")
		return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to load initial users: %v", err), hostIP, "alert")}, hostIP, err
	}
	slog.Debug("Loaded initial users", "count", len(initialUsers), "component", "system_monitor")
	if len(initialUsers) == 0 {
		// First run, save initial
		slog.Info("First run: initializing user file", "component", "system_monitor")
		if err := saveUsers(userInitialFile, currentUsers); err != nil {
			slog.Error("Failed to save initial users", "error", err, "component", "system_monitor")
			return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to save initial users: %v", err), hostIP, "alert")}, hostIP, err
		}
	} else {
		addedUsers, removedUsers := diffStrings(currentUsers, initialUsers)
		userMsg := ""
		if len(addedUsers) > 0 || len(removedUsers) > 0 {
			if len(addedUsers) > 0 {
				userMsg += "**增加的用户:**\n- " + strings.Join(addedUsers, "\n- ") + "\n"
			}
			if len(removedUsers) > 0 {
				userMsg += "**减少的用户:**\n- " + strings.Join(removedUsers, "\n- ") + "\n"
			}
			if userMsg != "" {
				slog.Info("Generating user change alert", "added", len(addedUsers), "removed", len(removedUsers), "component", "system_monitor")
				msgs = append(msgs, alertBot.FormatAlert(serviceName, "用户变更", userMsg, hostIP, "alert"))
				// Log change incrementally
				if err := logChange(changeLogFile, "user", addedUsers, removedUsers); err != nil {
					slog.Error("Failed to log user change", "error", err, "component", "system_monitor")
					return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to log user change: %v", err), hostIP, "alert")}, hostIP, err
				}
				// Refresh initialization data
				if err := saveUsers(userInitialFile, currentUsers); err != nil {
					slog.Error("Failed to update initial users", "error", err, "component", "system_monitor")
					return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to update initial users: %v", err), hostIP, "alert")}, hostIP, err
				}
			}
		} else {
			slog.Debug("No user changes detected", "added", len(addedUsers), "removed", len(removedUsers), "component", "system_monitor")
		}
	}

	// Monitor processes
	processInitialFile := ".psAll"
	currentProcesses, err := getCurrentProcesses()
	if err != nil {
		slog.Error("Failed to get current processes", "error", err, "component", "system_monitor")
		return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to get current processes: %v", err), hostIP, "alert")}, hostIP, err
	}
	slog.Debug("Retrieved current processes", "count", len(currentProcesses), "component", "system_monitor")
	initialProcesses, err := loadInitialProcesses(processInitialFile)
	if err != nil {
		slog.Error("Failed to load initial processes", "error", err, "component", "system_monitor")
		return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to load initial processes: %v", err), hostIP, "alert")}, hostIP, err
	}
	slog.Debug("Loaded initial processes", "count", len(initialProcesses), "component", "system_monitor")
	if len(initialProcesses) == 0 {
		// First run, save initial
		slog.Info("First run: initializing process file", "component", "system_monitor")
		if err := saveProcesses(processInitialFile, currentProcesses); err != nil {
			slog.Error("Failed to save initial processes", "error", err, "component", "system_monitor")
			return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to save initial processes: %v", err), hostIP, "alert")}, hostIP, err
		}
	} else {
		addedProcs, removedProcs := diffProcesses(currentProcesses, initialProcesses)
		procMsg := ""
		if len(addedProcs) > 0 || len(removedProcs) > 0 {
			if len(addedProcs) > 0 {
				procMsg += "**增加的进程:**\n| UID | PID | PPID | STIME | TTY | TIME | CMD |\n|-----|-----|------|-------|-----|------|-----|\n"
				for _, p := range addedProcs {
					procMsg += fmt.Sprintf("| %s | %d | %d | %s | %s | %s | %s |\n", p.User, p.PID, p.PPID, p.STIME, p.TTY, p.TIME, p.CMD)
				}
			}
			if len(removedProcs) > 0 {
				procMsg += "**减少的进程:**\n| UID | PID | PPID | STIME | TTY | TIME | CMD |\n|-----|-----|------|-------|-----|------|-----|\n"
				for _, p := range removedProcs {
					procMsg += fmt.Sprintf("| %s | %d | %d | %s | %s | %s | %s |\n", p.User, p.PID, p.PPID, p.STIME, p.TTY, p.TIME, p.CMD)
				}
			}
			if procMsg != "" {
				slog.Info("Generating process change alert", "added", len(addedProcs), "removed", len(removedProcs), "component", "system_monitor")
				msgs = append(msgs, alertBot.FormatAlert(serviceName, "进程变更", procMsg, hostIP, "alert"))
				// Log change incrementally
				if err := logChange(changeLogFile, "process", addedProcs, removedProcs); err != nil {
					slog.Error("Failed to log process change", "error", err, "component", "system_monitor")
					return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to log process change: %v", err), hostIP, "alert")}, hostIP, err
				}
				// Refresh initialization data
				if err := saveProcesses(processInitialFile, currentProcesses); err != nil {
					slog.Error("Failed to update initial processes", "error", err, "component", "system_monitor")
					return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to update initial processes: %v", err), hostIP, "alert")}, hostIP, err
				}
			}
		} else {
			slog.Debug("No process changes detected", "added", len(addedProcs), "removed", len(removedProcs), "component", "system_monitor")
		}
	}

	// Reinitialize if file size exceeds limit and no alerts
	if needsReinit && len(msgs) == 0 {
		if err := reinitializeSystemMonitoring(userInitialFile, processInitialFile, currentUsers, currentProcesses, changeLogFile); err != nil {
			slog.Error("Failed to reinitialize system monitoring", "error", err, "component", "system_monitor")
			return []string{alertBot.FormatAlert(serviceName, "服务异常", fmt.Sprintf("Failed to reinitialize system monitoring: %v", err), hostIP, "alert")}, hostIP, err
		}
	}

	if len(msgs) > 0 {
		return msgs, hostIP, fmt.Errorf("system issues")
	}
	return nil, "", nil
}

// logChange appends a change entry to the log file.
func logChange(file string, changeType string, added, removed any) error {
	var addedSlice, removedSlice []any
	switch changeType {
	case "user":
		if a, ok := added.([]string); ok {
			for _, v := range a {
				addedSlice = append(addedSlice, v)
			}
		}
		if r, ok := removed.([]string); ok {
			for _, v := range r {
				removedSlice = append(removedSlice, v)
			}
		}
	case "process":
		if a, ok := added.([]ProcessInfo); ok {
			for _, v := range a {
				addedSlice = append(addedSlice, v)
			}
		}
		if r, ok := removed.([]ProcessInfo); ok {
			for _, v := range r {
				removedSlice = append(removedSlice, v)
			}
		}
	default:
		return fmt.Errorf("unsupported change type: %s", changeType)
	}

	entry := ChangeLogEntry{
		Timestamp: time.Now(),
		Type:      changeType,
		Added:     addedSlice,
		Removed:   removedSlice,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		slog.Error("Failed to marshal change entry", "error", err, "component", "system_monitor")
		return err
	}
	data = append(data, '\n') // JSONL format
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Error("Failed to open change log file", "file", file, "error", err, "component", "system_monitor")
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		slog.Error("Failed to write change entry", "file", file, "error", err, "component", "system_monitor")
		return err
	}
	slog.Info("Logged change entry", "type", changeType, "added", len(addedSlice), "removed", len(removedSlice), "component", "system_monitor")
	return nil
}

// getCurrentUsers gets current system users from /etc/passwd.
func getCurrentUsers() ([]string, error) {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		slog.Error("Failed to read /etc/passwd", "error", err, "component", "system_monitor")
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	users := []string{}
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) > 0 {
			users = append(users, fields[0])
		}
	}
	sort.Strings(users)
	return users, nil
}

// loadInitialUsers loads users from file.
func loadInitialUsers(file string) ([]string, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		slog.Error("Failed to read user file", "file", file, "error", err, "component", "system_monitor")
		return nil, err
	}
	var info UserInfo
	if err := json.Unmarshal(data, &info); err != nil {
		slog.Error("Failed to unmarshal user data", "file", file, "error", err, "component", "system_monitor")
		return nil, err
	}
	return info.Users, nil
}

// saveUsers saves users to file.
func saveUsers(file string, users []string) error {
	info := UserInfo{Users: users}
	data, err := json.Marshal(info)
	if err != nil {
		slog.Error("Failed to marshal users", "error", err, "component", "system_monitor")
		return err
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		slog.Error("Failed to write user file", "file", file, "error", err, "component", "system_monitor")
		return err
	}
	slog.Debug("Saved users to file", "file", file, "count", len(users), "component", "system_monitor")
	return nil
}

// getCurrentProcesses gets current processes info.
func getCurrentProcesses() ([]ProcessInfo, error) {
	procs, err := process.Processes()
	if err != nil {
		slog.Error("Failed to get processes", "error", err, "component", "system_monitor")
		return nil, err
	}
	var infos []ProcessInfo
	for _, p := range procs {
		user, err := p.Username()
		if err != nil {
			user = "?"
			slog.Warn("Failed to get username for process", "pid", p.Pid, "error", err, "component", "system_monitor")
		}
		ppid, err := p.Ppid()
		if err != nil {
			ppid = 0
			slog.Warn("Failed to get ppid for process", "pid", p.Pid, "error", err, "component", "system_monitor")
		}
		createTime, err := p.CreateTime()
		if err != nil {
			createTime = 0
			slog.Warn("Failed to get create time for process", "pid", p.Pid, "error", err, "component", "system_monitor")
		}
		stime := time.UnixMilli(createTime).Format("Jan 02 15:04")
		tty, err := p.Terminal()
		if err != nil {
			tty = "?"
			slog.Warn("Failed to get terminal for process", "pid", p.Pid, "error", err, "component", "system_monitor")
		}
		times, err := p.Times()
		if err != nil {
			slog.Warn("Failed to get process times", "pid", p.Pid, "error", err, "component", "system_monitor")
			times = &cpu.TimesStat{}
		}
		total := times.User + times.System
		minutes := int(total) / 60
		seconds := int(total) % 60
		timeStr := fmt.Sprintf("%d:%02d", minutes, seconds)
		cmd, err := p.Cmdline()
		if err != nil {
			cmd, _ = p.Name()
			slog.Warn("Failed to get cmdline for process, using name", "pid", p.Pid, "error", err, "component", "system_monitor")
		}
		infos = append(infos, ProcessInfo{
			User:  user,
			PID:   p.Pid,
			PPID:  ppid,
			STIME: stime,
			TTY:   tty,
			TIME:  timeStr,
			CMD:   cmd,
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].PID < infos[j].PID })
	return infos, nil
}

// loadInitialProcesses loads processes from file.
func loadInitialProcesses(file string) ([]ProcessInfo, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return []ProcessInfo{}, nil
	}
	if err != nil {
		slog.Error("Failed to read process file", "file", file, "error", err, "component", "system_monitor")
		return nil, err
	}
	var infos []ProcessInfo
	if err := json.Unmarshal(data, &infos); err != nil {
		slog.Error("Failed to unmarshal process data", "file", file, "error", err, "component", "system_monitor")
		return nil, err
	}
	return infos, nil
}

// saveProcesses saves processes to file.
func saveProcesses(file string, procs []ProcessInfo) error {
	data, err := json.Marshal(procs)
	if err != nil {
		slog.Error("Failed to marshal processes", "error", err, "component", "system_monitor")
		return err
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		slog.Error("Failed to write process file", "file", file, "error", err, "component", "system_monitor")
		return err
	}
	slog.Debug("Saved processes to file", "file", file, "count", len(procs), "component", "system_monitor")
	return nil
}

// diffStrings finds added and removed strings.
func diffStrings(current, initial []string) (added, removed []string) {
	initialMap := make(map[string]bool)
	for _, s := range initial {
		initialMap[s] = true
	}
	for _, s := range current {
		if !initialMap[s] {
			added = append(added, s)
		}
	}
	currentMap := make(map[string]bool)
	for _, s := range current {
		currentMap[s] = true
	}
	for _, s := range initial {
		if !currentMap[s] {
			removed = append(removed, s)
		}
	}
	return
}

// diffProcesses finds added and removed processes (comparing by PID and CMD).
func diffProcesses(current, initial []ProcessInfo) (added, removed []ProcessInfo) {
	initialMap := make(map[string]bool)
	for _, p := range initial {
		key := fmt.Sprintf("%d:%s", p.PID, p.CMD)
		initialMap[key] = true
	}
	for _, p := range current {
		key := fmt.Sprintf("%d:%s", p.PID, p.CMD)
		if !initialMap[key] {
			added = append(added, p)
		}
	}
	currentMap := make(map[string]bool)
	for _, p := range current {
		key := fmt.Sprintf("%d:%s", p.PID, p.CMD)
		currentMap[key] = true
	}
	for _, p := range initial {
		key := fmt.Sprintf("%d:%s", p.PID, p.CMD)
		if !currentMap[key] {
			removed = append(removed, p)
		}
	}
	return
}

// shouldCleanup checks if cleanup is needed based on last cleanup time.
func shouldCleanup(lastCleanupFile string, interval time.Duration) bool {
	data, err := os.ReadFile(lastCleanupFile)
	if os.IsNotExist(err) {
		slog.Debug("Cleanup file does not exist, triggering cleanup", "file", lastCleanupFile, "component", "system_monitor")
		return true
	}
	if err != nil {
		slog.Error("Failed to read last cleanup file", "file", lastCleanupFile, "error", err, "component", "system_monitor")
		return true
	}
	var lastCleanup time.Time
	if err := json.Unmarshal(data, &lastCleanup); err != nil {
		slog.Error("Failed to unmarshal last cleanup time", "file", lastCleanupFile, "error", err, "component", "system_monitor")
		return true
	}
	return time.Since(lastCleanup) >= interval
}

// updateLastCleanup updates the last cleanup timestamp.
func updateLastCleanup(lastCleanupFile string) error {
	data, err := json.Marshal(time.Now())
	if err != nil {
		slog.Error("Failed to marshal last cleanup time", "error", err, "component", "system_monitor")
		return err
	}
	if err := os.WriteFile(lastCleanupFile, data, 0644); err != nil {
		slog.Error("Failed to write last cleanup file", "file", lastCleanupFile, "error", err, "component", "system_monitor")
		return err
	}
	slog.Debug("Updated last cleanup time", "file", lastCleanupFile, "component", "system_monitor")
	return nil
}

// cleanupHistoricalFiles removes compressed files older than retention period.
func cleanupHistoricalFiles(retentionPeriod time.Duration) error {
	now := time.Now()
	files, err := filepath.Glob("*.[0-9]{8}_[0-9]{6}.tar.gz")
	if err != nil {
		slog.Error("Failed to glob historical files", "error", err, "component", "system_monitor")
		return err
	}
	re := regexp.MustCompile(`\.(\d{8})_(\d{6})\.tar\.gz$`)
	for _, file := range files {
		matches := re.FindStringSubmatch(file)
		if len(matches) != 3 {
			continue
		}
		t, err := time.Parse("20060102_150405", matches[1]+"_"+matches[2])
		if err != nil {
			slog.Warn("Failed to parse timestamp in filename", "file", file, "error", err, "component", "system_monitor")
			continue
		}
		if now.Sub(t) > retentionPeriod {
			if err := os.Remove(file); err != nil {
				slog.Error("Failed to remove historical file", "file", file, "error", err, "component", "system_monitor")
				continue
			}
			slog.Info("Removed historical file", "file", file, "component", "system_monitor")
		}
	}
	return nil
}

// reinitializeSystemMonitoring archives old files and reinitializes monitoring.
func reinitializeSystemMonitoring(userInitialFile, processInitialFile string, currentUsers []string, currentProcesses []ProcessInfo, changeLogFile string) error {
	timestamp := time.Now().Format("20060102_150405")
	filesToArchive := []string{userInitialFile, processInitialFile, changeLogFile}

	// Create tar.gz archive
	archiveFile := fmt.Sprintf("archive_%s.tar.gz", timestamp)
	f, err := os.Create(archiveFile)
	if err != nil {
		slog.Error("Failed to create archive file", "file", archiveFile, "error", err, "component", "system_monitor")
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	for _, file := range filesToArchive {
		if _, err := os.Stat(file); os.IsNotExist(err) {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			slog.Error("Failed to read file for archiving", "file", file, "error", err, "component", "system_monitor")
			continue
		}
		hdr := &tar.Header{
			Name:    file + "." + timestamp,
			Mode:    0644,
			Size:    int64(len(data)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			slog.Error("Failed to write tar header", "file", file, "error", err, "component", "system_monitor")
			continue
		}
		if _, err := tw.Write(data); err != nil {
			slog.Error("Failed to write file to archive", "file", file, "error", err, "component", "system_monitor")
			continue
		}
		if err := os.Remove(file); err != nil {
			slog.Error("Failed to remove old file", "file", file, "error", err, "component", "system_monitor")
			continue
		}
		slog.Info("Archived and removed file", "file", file, "archive", archiveFile, "component", "system_monitor")
	}

	// Reinitialize files with current data
	if err := saveUsers(userInitialFile, currentUsers); err != nil {
		slog.Error("Failed to reinitialize user initial file", "file", userInitialFile, "error", err, "component", "system_monitor")
		return err
	}
	if err := saveProcesses(processInitialFile, currentProcesses); err != nil {
		slog.Error("Failed to reinitialize process initial file", "file", processInitialFile, "error", err, "component", "system_monitor")
		return err
	}
	// Create empty change log file
	if err := os.WriteFile(changeLogFile, []byte{}, 0644); err != nil {
		slog.Error("Failed to create empty change log file", "file", changeLogFile, "error", err, "component", "system_monitor")
		return err
	}
	slog.Info("Reinitialized system monitoring files", "component", "system_monitor")
	return nil
}