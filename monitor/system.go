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
	"sync"
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

// System monitors system users and processes for changes and sends alerts.
func System(ctx context.Context, cfg config.SystemConfig, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration) error {
	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "system")
		hostIP = "unknown"
	}

	// File size limit (500 MB)
	const maxFileSize = 500 * 1024 * 1024 // 500 MB in bytes
	const changeLogFile = ".changeLog.jsonl"
	const processLogFile = ".pslogs"
	const userInitialFile = ".userNumber"
	const processInitialFile = ".psAll"
	const lastCleanupFile = ".lastCleanup"
	filesToCheck := []string{changeLogFile, processLogFile}

	// Compile ignore patterns for processes
	var ignoreRegexps []*regexp.Regexp
	for _, pattern := range cfg.ProcessIgnorePatterns {
		rePattern := convertPatternToRegex(pattern)
		re, err := regexp.Compile(rePattern)
		if err != nil {
			slog.Warn("Invalid process ignore pattern", "pattern", pattern, "error", err, "component", "system")
			continue
		}
		ignoreRegexps = append(ignoreRegexps, re)
	}

	// Initialize details for alert message
	var details strings.Builder
	hasIssue := false

	// Initialize user change variables
	var addedUsers, removedUsers []string

	// Check and cleanup historical files every 30 days
	if shouldCleanup(lastCleanupFile, 30*24*time.Hour) {
		if err := cleanupHistoricalFiles(15 * 24 * time.Hour); err != nil {
			slog.Error("Failed to cleanup historical files", "error", err, "component", "system")
			details.WriteString(fmt.Sprintf("Failed to cleanup historical files: %v", err))
			if bot != nil {
				msg := bot.FormatAlert("System Alert", "Service Error", details.String(), hostIP, "alert")
				return sendSystemAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "System Alert", "Service Error", details.String(), hostIP, "alert", msg)
			}
			return fmt.Errorf("failed to cleanup historical files: %w", err)
		}
		if err := updateLastCleanup(lastCleanupFile); err != nil {
			slog.Error("Failed to update last cleanup time", "error", err, "component", "system")
			details.WriteString(fmt.Sprintf("Failed to update last cleanup time: %v", err))
			//details.WriteString(fmt.Sprintf("Failed to update last cleanup time: %v", err)
			if bot != nil {
				msg := bot.FormatAlert("System Alert", "Service Error", details.String(), hostIP, "alert")
				return sendSystemAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "System Alert", "Service Error", details.String(), hostIP, "alert", msg)
			}
			return fmt.Errorf("failed to update last cleanup time: %w", err)
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
	currentUsers, err := getCurrentUsers()
	if err != nil {
		slog.Error("Failed to get current users", "error", err, "component", "system")
		details.WriteString(fmt.Sprintf("Failed to get current users: %v", err))
		if bot != nil {
			msg := bot.FormatAlert("System Alert", "Service Error", details.String(), hostIP, "alert")
			return sendSystemAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "System Alert", "Service Error", details.String(), hostIP, "alert", msg)
		}
		return fmt.Errorf("failed to get current users: %w", err)
	}
	slog.Debug("Retrieved current users", "count", len(currentUsers), "component", "system")
	initialUsers, err := loadInitialUsers(userInitialFile)
	if err != nil {
		slog.Error("Failed to load initial users", "error", err, "component", "system")
		details.WriteString(fmt.Sprintf("Failed to load initial users: %v", err))
		if bot != nil {
			msg := bot.FormatAlert("System Alert", "Service Error", details.String(), hostIP, "alert")
			return sendSystemAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "System Alert", "Service Error", details.String(), hostIP, "alert", msg)
		}
		return fmt.Errorf("failed to load initial users: %w", err)
	}
	slog.Debug("Loaded initial users", "count", len(initialUsers), "component", "system")
	if len(initialUsers) == 0 {
		// First run, save initial
		slog.Info("First run: initializing user file", "component", "system")
		if err := saveUsers(userInitialFile, currentUsers); err != nil {
			slog.Error("Failed to save initial users", "error", err, "component", "system")
			details.WriteString(fmt.Sprintf("Failed to save initial users: %v", err))
			if bot != nil {
				msg := bot.FormatAlert("System Alert", "Service Error", details.String(), hostIP, "alert")
				return sendSystemAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "System Alert", "Service Error", details.String(), hostIP, "alert", msg)
			}
			return fmt.Errorf("failed to save initial users: %w", err)
		}
	} else {
		addedUsers, removedUsers = diffStrings(currentUsers, initialUsers)
		if len(addedUsers) > 0 || len(removedUsers) > 0 {
			hasIssue = true
			if len(addedUsers) > 0 {
				fmt.Fprintf(&details, "**✅⊕Added Users⊕**:\n- %s\n", strings.Join(addedUsers, "\n- "))
			}
			if len(removedUsers) > 0 {
				fmt.Fprintf(&details, "**❌⊖Removed Users⊖**:\n- %s\n", strings.Join(removedUsers, "\n- "))
			}
			slog.Info("Detected user changes", "added_users", addedUsers, "removed_users", removedUsers, "component", "system")
		}
	}

	// Monitor processes
	currentProcesses, err := getCurrentProcesses()
	if err != nil {
		slog.Error("Failed to get current processes", "error", err, "component", "system")
		details.WriteString(fmt.Sprintf("Failed to get current processes: %v", err))
		if bot != nil {
			msg := bot.FormatAlert("System Alert", "Service Error", details.String(), hostIP, "alert")
			return sendSystemAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "System Alert", "Service Error", details.String(), hostIP, "alert", msg)
		}
		return fmt.Errorf("failed to get current processes: %w", err)
	}
	slog.Debug("Retrieved current processes", "count", len(currentProcesses), "component", "system")
	initialProcesses, err := loadInitialProcesses(processInitialFile)
	if err != nil {
		slog.Error("Failed to load initial processes", "error", err, "component", "system")
		details.WriteString(fmt.Sprintf("Failed to load initial processes: %v", err))
		if bot != nil {
			msg := bot.FormatAlert("System Alert", "Service Error", details.String(), hostIP, "alert")
			return sendSystemAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "System Alert", "Service Error", details.String(), hostIP, "alert", msg)
		}
		return fmt.Errorf("failed to load initial processes: %w", err)
	}
	slog.Debug("Loaded initial processes", "count", len(initialProcesses), "component", "system")
	if len(initialProcesses) == 0 {
		// First run, save initial
		slog.Info("First run: initializing process file", "component", "system")
		if err := saveProcesses(processInitialFile, currentProcesses); err != nil {
			slog.Error("Failed to save initial processes", "error", err, "component", "system")
			details.WriteString(fmt.Sprintf("Failed to save initial processes: %v", err))
			if bot != nil {
				msg := bot.FormatAlert("System Alert", "Service Error", details.String(), hostIP, "alert")
				return sendSystemAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "System Alert", "Service Error", details.String(), hostIP, "alert", msg)
			}
			return fmt.Errorf("failed to save initial processes: %w", err)
		}
	} else {
		addedProcesses, removedProcesses = diffProcesses(currentProcesses, initialProcesses)
		// Filter added and removed processes
		filteredAdded, ignoredAdded := filterProcesses(addedProcesses, ignoreRegexps)
		filteredRemoved, ignoredRemoved := filterProcesses(removedProcesses, ignoreRegexps)
		if len(filteredAdded) > 0 || len(filteredRemoved) > 0 {
			hasIssue = true
			if len(filteredAdded) > 0 {
				fmt.Fprintf(&details, "**✅⊕Added Processes⊕**:\n%s\n", formatProcesses(filteredAdded))
			}
			if len(filteredRemoved) > 0 {
				fmt.Fprintf(&details, "**❌⊖Removed Processes⊖**:\n%s\n", formatProcesses(filteredRemoved))
			}
			slog.Info("Detected process changes (after filtering)", "added_processes", len(filteredAdded), "removed_processes", len(filteredRemoved), "component", "system")
		}
		// Log ignored changes to local file
		if len(ignoredAdded) > 0 || len(ignoredRemoved) > 0 {
			logIgnoredChange(changeLogFile, "process", ignoredAdded, ignoredRemoved)
			slog.Debug("Logged ignored process changes", "ignored_added", len(ignoredAdded), "ignored_removed", len(ignoredRemoved), "component", "system")
		}
	}

	// If reinitialization is needed, archive old files
	if needsReinit {
		slog.Info("Reinitializing system monitoring due to file size limit", "component", "system")
		if err := reinitializeSystemMonitoring(userInitialFile, processInitialFile, currentUsers, currentProcesses, changeLogFile, processLogFile); err != nil {
			slog.Error("Failed to reinitialize system monitoring", "error", err, "component", "system")
			details.WriteString(fmt.Sprintf("Failed to reinitialize system monitoring: %v", err))
			if bot != nil {
				msg := bot.FormatAlert("System Alert", "Service Error", details.String(), hostIP, "alert")
				return sendSystemAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "System Alert", "Service Error", details.String(), hostIP, "alert", msg)
			}
			return fmt.Errorf("failed to reinitialize system monitoring: %w", err)
		}
	}

	// Send alert if issues detected
	if hasIssue {
		if bot != nil {
			msg := bot.FormatAlert("System Alert", "Change Detected", details.String(), hostIP, "alert")
			return sendSystemAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "System Alert", "Change Detected", details.String(), hostIP, "alert", msg)
		}
		return fmt.Errorf("system issues detected: %s", details.String())
	}

	slog.Debug("No system issues detected", "component", "system")
	return nil
}

// convertPatternToRegex converts a pattern to a regex pattern.
func convertPatternToRegex(pattern string) string {
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		return regexp.QuoteMeta(pattern[1 : len(pattern)-1])
	} else if strings.HasSuffix(pattern, "*") {
		return "^" + regexp.QuoteMeta(pattern[:len(pattern)-1]) + ".*$"
	} else if strings.HasPrefix(pattern, "*") {
		return ".*" + regexp.QuoteMeta(pattern[1:]) + "$"
	}
	return "^" + regexp.QuoteMeta(pattern) + "$"
}

// filterProcesses filters processes based on ignore patterns.
func filterProcesses(processes []ProcessInfo, ignoreRegexps []*regexp.Regexp) (filtered, ignored []ProcessInfo) {
	for _, p := range processes {
		ignoredFlag := false
		for _, re := range ignoreRegexps {
			if re.MatchString(p.CMD) {
				ignoredFlag = true
				break
			}
		}
		if ignoredFlag {
			ignored = append(ignored, p)
		} else {
			filtered = append(filtered, p)
		}
	}
	return
}

// formatProcesses formats process info for alert message.
func formatProcesses(processes []ProcessInfo) string {
	var sb strings.Builder
	for _, p := range processes {
		fmt.Fprintf(&sb, "- %s (PID: %d, CMD: %s)\n", p.User, p.PID, p.CMD)
	}
	return sb.String()
}

// logIgnoredChange logs ignored changes to the change log file.
func logIgnoredChange(changeLogFile, changeType string, added, removed []any) {
	entry := ChangeLogEntry{
		Timestamp: time.Now(),
		Type:      changeType,
		Added:     added,
		Removed:   removed,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		slog.Error("Failed to marshal ignored change entry", "error", err, "component", "system")
		return
	}
	f, err := os.OpenFile(changeLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Error("Failed to open change log file", "file", changeLogFile, "error", err, "component", "system")
		return
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		slog.Error("Failed to write ignored change entry", "error", err, "component", "system")
	}
}

// sendSystemAlert sends a deduplicated Telegram alert for the System module.
func sendSystemAlert(ctx context.Context, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, serviceName, eventName, details, hostIP, alertType, message string) error {
	hash, err := util.MD5Hash(details)
	if err != nil {
		slog.Error("Failed to generate alert hash", "error", err, "component", "system")
		return fmt.Errorf("failed to generate alert hash: %w", err)
	}
	cacheMutex.Lock()
	now := time.Now()
	if timestamp, ok := alertCache[hash]; ok && now.Sub(timestamp) < alertSilenceDuration {
		slog.Info("Skipping duplicate alert", "hash", hash, "component", "system")
		cacheMutex.Unlock()
		return nil
	}
	alertCache[hash] = now
	// Clean up old cache entries
	for h, t := range alertCache {
		if now.Sub(t) >= alertSilenceDuration {
			delete(alertCache, h)
			slog.Debug("Removed expired alert cache entry", "hash", h, "component", "system")
		}
	}
	cacheMutex.Unlock()
	slog.Debug("Sending alert", "message", message, "component", "system")
	if err := bot.SendAlert(ctx, serviceName, eventName, details, hostIP, alertType); err != nil {
		slog.Error("Failed to send alert", "error", err, "component", "system")
		return fmt.Errorf("failed to send alert: %w", err)
	}
	slog.Info("Sent alert", "message", message, "component", "system")
	return nil
}

// getCurrentUsers retrieves the current list of users.
func getCurrentUsers() ([]string, error) {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil, fmt.Errorf("failed to read /etc/passwd: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	var users []string
	for _, line := range lines {
		if line == "" {
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

// loadInitialUsers loads the initial list of users from file.
func loadInitialUsers(file string) ([]string, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, nil // No initial file yet
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read initial users file: %w", err)
	}
	var info UserInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal initial users: %w", err)
	}
	return info.Users, nil
}

// saveUsers saves the list of users to file.
func saveUsers(file string, users []string) error {
	info := UserInfo{Users: users}
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("failed to marshal users: %w", err)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		return fmt.Errorf("failed to write users file: %w", err)
	}
	return nil
}

// diffStrings returns added and removed strings between current and initial.
func diffStrings(current, initial []string) (added, removed []string) {
	initialMap := make(map[string]bool)
	for _, u := range initial {
		initialMap[u] = true
	}
	for _, u := range current {
		if !initialMap[u] {
			added = append(added, u)
		}
	}
	currentMap := make(map[string]bool)
	for _, u := range current {
		currentMap[u] = true
	}
	for _, u := range initial {
		if !currentMap[u] {
			removed = append(removed, u)
		}
	}
	return
}

// getCurrentProcesses retrieves the current list of processes.
func getCurrentProcesses() ([]ProcessInfo, error) {
	processes, err := process.Processes()
	if err != nil {
		return nil, fmt.Errorf("failed to get processes: %w", err)
	}
	var ps []ProcessInfo
	for _, p := range processes {
		user, _ := p.Username()
		ppid, _ := p.Ppid()
		createTime, _ := p.CreateTime()
		stime := time.UnixMilli(createTime).Format("15:04:05")
		tty, _ := p.Terminal()
		cmd, _ := p.Cmdline()
		cpuTimes, _ := p.Times()
		totalTime := cpuTimes.User + cpuTimes.System
		timeStr := fmt.Sprintf("%d:%02d", int(totalTime/60), int(totalTime)%60)
		ps = append(ps, ProcessInfo{
			User:  user,
			PID:   p.Pid,
			PPID:  ppid,
			STIME: stime,
			TTY:   tty,
			TIME:  timeStr,
			CMD:   cmd,
		})
	}
	sort.Slice(ps, func(i, j int) bool {
		return ps[i].PID < ps[j].PID
	})
	return ps, nil
}

// loadInitialProcesses loads the initial list of processes from file.
func loadInitialProcesses(file string) ([]ProcessInfo, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, nil // No initial file yet
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read initial processes file: %w", err)
	}
	var ps []ProcessInfo
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("failed to unmarshal initial processes: %w", err)
	}
	return ps, nil
}

// saveProcesses saves the list of processes to file.
func saveProcesses(file string, ps []ProcessInfo) error {
	data, err := json.Marshal(ps)
	if err != nil {
		return fmt.Errorf("failed to marshal processes: %w", err)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		return fmt.Errorf("failed to write processes file: %w", err)
	}
	return nil
}

// diffProcesses returns added and removed processes between current and initial.
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
		slog.Debug("Cleanup file does not exist, triggering cleanup", "file", lastCleanupFile, "component", "system")
		return true
	}
	if err != nil {
		slog.Error("Failed to read last cleanup file", "file", lastCleanupFile, "error", err, "component", "system")
		return true
	}
	var lastCleanup time.Time
	if err := json.Unmarshal(data, &lastCleanup); err != nil {
		slog.Error("Failed to unmarshal last cleanup time", "file", lastCleanupFile, "error", err, "component", "system")
		return true
	}
	return time.Since(lastCleanup) >= interval
}

// updateLastCleanup updates the last cleanup timestamp.
func updateLastCleanup(lastCleanupFile string) error {
	data, err := json.Marshal(time.Now())
	if err != nil {
		slog.Error("Failed to marshal last cleanup time", "error", err, "component", "system")
		return fmt.Errorf("failed to marshal last cleanup time: %w", err)
	}
	if err := os.WriteFile(lastCleanupFile, data, 0644); err != nil {
		slog.Error("Failed to write last cleanup file", "file", lastCleanupFile, "error", err, "component", "system")
		return fmt.Errorf("failed to write last cleanup file: %w", err)
	}
	slog.Debug("Updated last cleanup time", "file", lastCleanupFile, "component", "system")
	return nil
}

// cleanupHistoricalFiles removes compressed files older than retention period.
func cleanupHistoricalFiles(retentionPeriod time.Duration) error {
	now := time.Now()
	files, err := filepath.Glob("*.[0-9]{8}_[0-9]{6}.tar.gz")
	if err != nil {
		slog.Error("Failed to glob historical files", "error", err, "component", "system")
		return fmt.Errorf("failed to glob historical files: %w", err)
	}
	re := regexp.MustCompile(`\.(\d{8})_(\d{6})\.tar\.gz$`)
	for _, file := range files {
		matches := re.FindStringSubmatch(file)
		if len(matches) != 3 {
			continue
		}
		t, err := time.Parse("20060102_150405", matches[1]+"_"+matches[2])
		if err != nil {
			slog.Warn("Failed to parse timestamp in filename", "file", file, "error", err, "component", "system")
			continue
		}
		if now.Sub(t) > retentionPeriod {
			if err := os.Remove(file); err != nil {
				slog.Error("Failed to remove historical file", "file", file, "error", err, "component", "system")
				continue
			}
			slog.Info("Removed historical file", "file", file, "component", "system")
		}
	}
	return nil
}

// reinitializeSystemMonitoring archives old files and reinitializes monitoring.
func reinitializeSystemMonitoring(userInitialFile, processInitialFile string, currentUsers []string, currentProcesses []ProcessInfo, changeLogFile, processLogFile string) error {
	timestamp := time.Now().Format("20060102_150405")
	filesToArchive := []string{userInitialFile, processInitialFile, changeLogFile, processLogFile}

	// Create tar.gz archive
	archiveFile := fmt.Sprintf("archive_%s.tar.gz", timestamp)
	f, err := os.Create(archiveFile)
	if err != nil {
		slog.Error("Failed to create archive file", "file", archiveFile, "error", err, "component", "system")
		return fmt.Errorf("failed to create archive file: %w", err)
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
			slog.Error("Failed to read file for archiving", "file", file, "error", err, "component", "system")
			continue
		}
		hdr := &tar.Header{
			Name:    file + "." + timestamp,
			Mode:    0644,
			Size:    int64(len(data)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			slog.Error("Failed to write tar header", "file", file, "error", err, "component", "system")
			continue
		}
		if _, err := tw.Write(data); err != nil {
			slog.Error("Failed to write file to archive", "file", file, "error", err, "component", "system")
			continue
		}
		if err := os.Remove(file); err != nil {
			slog.Error("Failed to remove old file", "file", file, "error", err, "component", "system")
			continue
		}
		slog.Info("Archived and removed file", "file", file, "archive", archiveFile, "component", "system")
	}

	// Reinitialize files with current data
	if err := saveUsers(userInitialFile, currentUsers); err != nil {
		slog.Error("Failed to reinitialize user initial file", "file", userInitialFile, "error", err, "component", "system")
		return fmt.Errorf("failed to reinitialize user initial file: %w", err)
	}
	if err := saveProcesses(processInitialFile, currentProcesses); err != nil {
		slog.Error("Failed to reinitialize process initial file", "file", processInitialFile, "error", err, "component", "system")
		return fmt.Errorf("failed to reinitialize process initial file: %w", err)
	}
	// Create empty change log and process log files
	for _, file := range []string{changeLogFile, processLogFile} {
		if err := os.WriteFile(file, []byte{}, 0644); err != nil {
			slog.Error("Failed to create empty log file", "file", file, "error", err, "component", "system")
			return fmt.Errorf("failed to create empty log file %s: %w", file, err)
		}
	}
	slog.Info("Reinitialized system monitoring files", "component", "system")
	return nil
}