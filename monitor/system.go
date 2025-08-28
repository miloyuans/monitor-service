package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	"github.com/yourusername/monitor-service/config"
)

// UserInfo holds initial user information.
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

	// Monitor users
	userFile := ".userNumber"
	initialUsers, err := loadInitialUsers(userFile)
	if err != nil {
		slog.Error("Failed to load initial users", "error", err)
		return []string{fmt.Sprintf("%s: Failed to load initial users: %v", clusterPrefix, err)}, err
	}
	currentUsers, err := getCurrentUsers()
	if err != nil {
		slog.Error("Failed to get current users", "error", err)
		return []string{fmt.Sprintf("%s: Failed to get current users: %v", clusterPrefix, err)}, err
	}
	if len(initialUsers) == 0 {
		// First run, save initial
		if err := saveUsers(userFile, currentUsers); err != nil {
			slog.Error("Failed to save initial users", "error", err)
			return []string{fmt.Sprintf("%s: Failed to save initial users: %v", clusterPrefix, err)}, err
		}
	} else {
		addedUsers, removedUsers := diffStrings(currentUsers, initialUsers)
		userMsg := ""
		if len(addedUsers) > 0 {
			userMsg += "**增加的用户:**\n" + strings.Join(addedUsers, "\n- ") + "\n"
		}
		if len(removedUsers) > 0 {
			userMsg += "**减少的用户:**\n" + strings.Join(removedUsers, "\n- ") + "\n"
		}
		if userMsg != "" {
			msgs = append(msgs, fmt.Sprintf("%s: 用户变更:\n%s", clusterPrefix, userMsg))
		}
	}

	// Monitor processes
	processFile := ".psAll"
	initialProcesses, err := loadInitialProcesses(processFile)
	if err != nil {
		slog.Error("Failed to load initial processes", "error", err)
		return []string{fmt.Sprintf("%s: Failed to load initial processes: %v", clusterPrefix, err)}, err
	}
	currentProcesses, err := getCurrentProcesses()
	if err != nil {
		slog.Error("Failed to get current processes", "error", err)
		return []string{fmt.Sprintf("%s: Failed to get current processes: %v", clusterPrefix, err)}, err
	}
	if len(initialProcesses) == 0 {
		// First run, save initial
		if err := saveProcesses(processFile, currentProcesses); err != nil {
			slog.Error("Failed to save initial processes", "error", err)
			return []string{fmt.Sprintf("%s: Failed to save initial processes: %v", clusterPrefix, err)}, err
		}
	} else {
		addedProcs, removedProcs := diffProcesses(currentProcesses, initialProcesses)
		procMsg := ""
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

// loadInitialUsers loads initial users from file.
func loadInitialUsers(file string) ([]string, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var info UserInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return info.Users, nil
}

// saveUsers saves users to file.
func saveUsers(file string, users []string) error {
	info := UserInfo{Users: users}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0644)
}

// getCurrentProcesses gets current processes info.
func getCurrentProcesses() ([]ProcessInfo, error) {
	procs, err := process.Processes()
	if err != nil {
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
			times = &process.TimesStat{}
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

// loadInitialProcesses loads initial processes from file.
func loadInitialProcesses(file string) ([]ProcessInfo, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return []ProcessInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	var infos []ProcessInfo
	if err := json.Unmarshal(data, &infos); err != nil {
		return nil, err
	}
	return infos, nil
}

// saveProcesses saves processes to file.
func saveProcesses(file string, procs []ProcessInfo) error {
	data, err := json.Marshal(procs)
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0644)
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

// diffProcesses finds added and removed processes (comparing by all fields).
func diffProcesses(current, initial []ProcessInfo) (added, removed []ProcessInfo) {
	initialMap := make(map[string]bool)
	for _, p := range initial {
		key := fmt.Sprintf("%s:%d:%d:%s:%s:%s:%s", p.User, p.PID, p.PPID, p.STIME, p.TTY, p.TIME, p.CMD)
		initialMap[key] = true
	}
	for _, p := range current {
		key := fmt.Sprintf("%s:%d:%d:%s:%s:%s:%s", p.User, p.PID, p.PPID, p.STIME, p.TTY, p.TIME, p.CMD)
		if !initialMap[key] {
			added = append(added, p)
		}
	}
	currentMap := make(map[string]bool)
	for _, p := range current {
		key := fmt.Sprintf("%s:%d:%d:%s:%s:%s:%s", p.User, p.PID, p.PPID, p.STIME, p.TTY, p.TIME, p.CMD)
		currentMap[key] = true
	}
	for _, p := range initial {
		key := fmt.Sprintf("%s:%d:%d:%s:%s:%s:%s", p.User, p.PID, p.PPID, p.STIME, p.TTY, p.TIME, p.CMD)
		if !currentMap[key] {
			removed = append(removed, p)
		}
	}
	return
}