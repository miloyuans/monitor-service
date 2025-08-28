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
	"monitor-service/config"
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

// System monitors system users and processes for changes.
func System(ctx context.Context, cfg config.SystemConfig, clusterName string) ([]string, error) {
	msgs := []string{}
	clusterPrefix := fmt.Sprintf("**System (%s)**", clusterName)

	// File size limit (500 MB)
	const maxFileSize = 500 * 1024 * 1024 // 500 MB in bytes
	// Files to check
	filesToCheck := []string{".userNumber", ".psAll"}

	// Check and cleanup historical files every 30 days
	lastCleanupFile := ".lastCleanup"
	if shouldCleanup(lastCleanupFile, 30*24*time.Hour) {
		if err := cleanupHistoricalFiles(15 * 24 * time.Hour); err != nil {
			slog.Error("Failed to cleanup historical files", "error", err)
			return []string{fmt.Sprintf("%s: Failed to cleanup historical files: %v", clusterPrefix, err)}, err
		}
		if err := updateLastCleanup(lastCleanupFile); err != nil {
			slog.Error("Failed to update last cleanup time", "error", err)
			return []string{fmt.Sprintf("%s: Failed to update last cleanup time: %v", clusterPrefix, err)}, err
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

	// Monitor users
	userInitialFile := ".userNumber"
	currentUsers, err := getCurrentUsers()
	if err != nil {
		slog.Error("Failed to get current users", "error", err)
		return []string{fmt.Sprintf("%s: Failed to get current users: %v", clusterPrefix, err)}, err
	}
	initialUsers, err := loadInitialUsers(userInitialFile)
	if err != nil {
		slog.Error("Failed to load initial users", "error", err)
		return []string{fmt.Sprintf("%s: Failed to load initial users: %v", clusterPrefix, err)}, err
	}
	if len(initialUsers) == 0 {
		// First run, save initial
		if err := saveUsers(userInitialFile, currentUsers); err != nil {
			slog.Error("Failed to save initial users", "error", err)
			return []string{fmt.Sprintf("%s: Failed to save initial users: %v", clusterPrefix, err)}, err
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
				msgs = append(msgs, fmt.Sprintf("%s: 用户变更:\n%s", clusterPrefix, userMsg))
				// Update initial users file to current
				if err := saveUsers(userInitialFile, currentUsers); err != nil {
					slog.Error("Failed to update initial users", "error", err)
					return []string{fmt.Sprintf("%s: Failed to update initial users: %v", clusterPrefix, err)}, err
				}
			}
		}
	}

	// Monitor processes
	processInitialFile := ".psAll"
	currentProcesses, err := getCurrentProcesses()
	if err != nil {
		slog.Error("Failed to get current processes", "error", err)
		return []string{fmt.Sprintf("%s: Failed to get current processes: %v", clusterPrefix, err)}, err
	}
	initialProcesses, err := loadInitialProcesses(processInitialFile)
	if err != nil {
		slog.Error("Failed to load initial processes", "error", err)
		return []string{fmt.Sprintf("%s: Failed to load initial processes: %v", clusterPrefix, err)}, err
	}
	if len(initialProcesses) == 0 {
		// First run, save initial
		if err := saveProcesses(processInitialFile, currentProcesses); err != nil {
			slog.Error("Failed to save initial processes", "error", err)
			return []string{fmt.Sprintf("%s: Failed to save initial processes: %v", clusterPrefix, err)}, err
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
				msgs = append(msgs, fmt.Sprintf("%s: 进程变更:\n%s", clusterPrefix, procMsg))
				// Update initial processes file to current
				if err := saveProcesses(processInitialFile, currentProcesses); err != nil {
					slog.Error("Failed to update initial processes", "error", err)
					return []string{fmt.Sprintf("%s: Failed to update initial processes: %v", clusterPrefix, err)}, err
				}
			}
		}
	}

	// Reinitialize if file size exceeds limit and no alerts
	if needsReinit && len(msgs) == 0 {
		if err := reinitializeSystemMonitoring(userInitialFile, processInitialFile, currentUsers, currentProcesses); err != nil {
			slog.Error("Failed to reinitialize system monitoring", "error", err)
			return []string{fmt.Sprintf("%s: Failed to reinitialize system monitoring: %v", clusterPrefix, err)}, err
		}
	}

	if len(msgs) > 0 {
		return msgs, fmt.Errorf("system issues")
	}
	return nil, nil
}

// getCurrentUsers gets current system users from /etc/passwd.
func getCurrentUsers() ([]string, error) {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		slog.Error("Failed to read /etc/passwd", "error", err)
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
		slog.Error("Failed to read user file", "file", file, "error", err)
		return nil, err
	}
	var info UserInfo
	if err := json.Unmarshal(data, &info); err != nil {
		slog.Error("Failed to unmarshal user data", "file", file, "error", err)
		return nil, err
	}
	return info.Users, nil
}

// saveUsers saves users to file.
func saveUsers(file string, users []string) error {
	info := UserInfo{Users: users}
	data, err := json.Marshal(info)
	if err != nil {
		slog.Error("Failed to marshal users", "error", err)
		return err
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		slog.Error("Failed to write user file", "file", file, "error", err)
		return err
	}
	return nil
}

// getCurrentProcesses gets current processes info.
func getCurrentProcesses() ([]ProcessInfo, error) {
	procs, err := process.Processes()
	if err != nil {
		slog.Error("Failed to get processes", "error", err)
		return nil, err
	}
	var infos []ProcessInfo
	for _, p := range procs {
		user, err := p.Username()
		if err != nil {
			user = "?"
		}
		ppid, err := p.Ppid()
		if err != nil {
			ppid = 0
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
		times, err := p.Times()
		if err != nil {
			slog.Warn("Failed to get process times", "pid", p.Pid, "error", err)
			times = &cpu.TimesStat{}
		}
		total := times.User + times.System
		minutes := int(total) / 60
		seconds := int(total) % 60
		timeStr := fmt.Sprintf("%d:%02d", minutes, seconds)
		cmd, err := p.Cmdline()
		if err != nil {
			cmd, _ = p.Name()
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
		slog.Error("Failed to read process file", "file", file, "error", err)
		return nil, err
	}
	var infos []ProcessInfo
	if err := json.Unmarshal(data, &infos); err != nil {
		slog.Error("Failed to unmarshal process data", "file", file, "error", err)
		return nil, err
	}
	return infos, nil
}

// saveProcesses saves processes to file.
func saveProcesses(file string, procs []ProcessInfo) error {
	data, err := json.Marshal(procs)
	if err != nil {
		slog.Error("Failed to marshal processes", "error", err)
		return err
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		slog.Error("Failed to write process file", "file", file, "error", err)
		return err
	}
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
		return true
	}
	if err != nil {
		slog.Error("Failed to read last cleanup file", "file", lastCleanupFile, "error", err)
		return true
	}
	var lastCleanup time.Time
	if err := json.Unmarshal(data, &lastCleanup); err != nil {
		slog.Error("Failed to unmarshal last cleanup time", "file", lastCleanupFile, "error", err)
		return true
	}
	return time.Since(lastCleanup) >= interval
}

// updateLastCleanup updates the last cleanup timestamp.
func updateLastCleanup(lastCleanupFile string) error {
	data, err := json.Marshal(time.Now())
	if err != nil {
		slog.Error("Failed to marshal last cleanup time", "error", err)
		return err
	}
	if err := os.WriteFile(lastCleanupFile, data, 0644); err != nil {
		slog.Error("Failed to write last cleanup file", "file", lastCleanupFile, "error", err)
		return err
	}
	return nil
}

// cleanupHistoricalFiles removes compressed files older than retention period.
func cleanupHistoricalFiles(retentionPeriod time.Duration) error {
	now := time.Now()
	files, err := filepath.Glob("*.[0-9]{8}_[0-9]{6}.tar.gz")
	if err != nil {
		slog.Error("Failed to glob historical files", "error", err)
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
			slog.Warn("Failed to parse timestamp in filename", "file", file, "error", err)
			continue
		}
		if now.Sub(t) > retentionPeriod {
			if err := os.Remove(file); err != nil {
				slog.Error("Failed to remove historical file", "file", file, "error", err)
				continue
			}
			slog.Info("Removed historical file", "file", file)
		}
	}
	return nil
}

// reinitializeSystemMonitoring archives old files and reinitializes monitoring.
func reinitializeSystemMonitoring(userInitialFile, processInitialFile string, currentUsers []string, currentProcesses []ProcessInfo) error {
	timestamp := time.Now().Format("20060102_150405")
	filesToArchive := []string{userInitialFile, processInitialFile}

	// Create tar.gz archive
	archiveFile := fmt.Sprintf("archive_%s.tar.gz", timestamp)
	f, err := os.Create(archiveFile)
	if err != nil {
		slog.Error("Failed to create archive file", "file", archiveFile, "error", err)
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
			slog.Error("Failed to read file for archiving", "file", file, "error", err)
			continue
		}
		hdr := &tar.Header{
			Name:    file + "." + timestamp,
			Mode:    0644,
			Size:    int64(len(data)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			slog.Error("Failed to write tar header", "file", file, "error", err)
			continue
		}
		if _, err := tw.Write(data); err != nil {
			slog.Error("Failed to write file to archive", "file", file, "error", err)
			continue
		}
		if err := os.Remove(file); err != nil {
			slog.Error("Failed to remove old file", "file", file, "error", err)
			continue
		}
		slog.Info("Archived and removed file", "file", file, "archive", archiveFile)
	}

	// Reinitialize files with current data
	if err := saveUsers(userInitialFile, currentUsers); err != nil {
		slog.Error("Failed to reinitialize user initial file", "file", userInitialFile, "error", err)
		return err
	}
	if err := saveProcesses(processInitialFile, currentProcesses); err != nil {
		slog.Error("Failed to reinitialize process initial file", "file", processInitialFile, "error", err)
		return err
	}
	slog.Info("Reinitialized system monitoring files")
	return nil
}