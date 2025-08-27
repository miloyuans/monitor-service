package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
	"github.com/yourusername/monitor-service/config"
)

// UserInfo holds initial user information.
type UserInfo struct {
	Users []string `json:"users"`
}

// ProcessInfo holds process details.
type ProcessInfo struct {
	PID      int32  `json:"pid"`
	Name     string `json:"name"`
	Cmdline  string `json:"cmdline"`
	Username string `json:"username"`
}

// System monitors system users and processes for changes.
func System(ctx context.Context, cfg config.SystemConfig) ([]string, error) {
	msgs := []string{}

	// Monitor users
	userFile := ".userNumber"
	initialUsers, err := loadInitialUsers(userFile)
	if err != nil {
		return []string{fmt.Sprintf("**System**: Failed to load initial users: %v", err)}, err
	}
	currentUsers, err := getCurrentUsers()
	if err != nil {
		return []string{fmt.Sprintf("**System**: Failed to get current users: %v", err)}, err
	}
	if len(initialUsers) == 0 {
		// First run, save initial
		if err := saveUsers(userFile, currentUsers); err != nil {
			return []string{fmt.Sprintf("**System**: Failed to save initial users: %v", err)}, err
		}
	} else {
		addedUsers, removedUsers := diffStrings(currentUsers, initialUsers)
		if len(addedUsers) > 0 || len(removedUsers) > 0 {
			userMsg := "**System Users Change**:\n"
			if len(addedUsers) > 0 {
				userMsg += fmt.Sprintf("Added users: %s\n", strings.Join(addedUsers, ", "))
			}
			if len(removedUsers) > 0 {
				userMsg += fmt.Sprintf("Removed users: %s\n", strings.Join(removedUsers, ", "))
			}
			msgs = append(msgs, userMsg)
		}
	}

	// Monitor processes
	processFile := ".psAll"
	initialProcesses, err := loadInitialProcesses(processFile)
	if err != nil {
		return []string{fmt.Sprintf("**System**: Failed to load initial processes: %v", err)}, err
	}
	currentProcesses, err := getCurrentProcesses()
	if err != nil {
		return []string{fmt.Sprintf("**System**: Failed to get current processes: %v", err)}, err
	}
	if len(initialProcesses) == 0 {
		// First run, save initial
		if err := saveProcesses(processFile, currentProcesses); err != nil {
			return []string{fmt.Sprintf("**System**: Failed to save initial processes: %v", err)}, err
		}
	} else {
		addedProcs, removedProcs := diffProcesses(currentProcesses, initialProcesses)
		if len(addedProcs) > 0 || len(removedProcs) > 0 {
			procMsg := "**System Processes Change**:\n\n| PID | Name | Cmdline | Username |\n|-----|------|---------|----------|\n"
			if len(addedProcs) > 0 {
				procMsg += "**Added Processes:**\n"
				for _, p := range addedProcs {
					procMsg += fmt.Sprintf("| %d | %s | %s | %s |\n", p.PID, p.Name, p.Cmdline, p.Username)
				}
			}
			if len(removedProcs) > 0 {
				procMsg += "**Removed Processes:**\n"
				for _, p := range removedProcs {
					procMsg += fmt.Sprintf("| %d | %s | %s | %s |\n", p.PID, p.Name, p.Cmdline, p.Username)
				}
			}
			msgs = append(msgs, procMsg)
		}
	}

	if len(msgs) > 0 {
		return msgs, fmt.Errorf("system issues")
	}
	return nil, nil
}

// getCurrentUsers gets current logged-in users.
func getCurrentUsers() ([]string, error) {
	out, err := exec.Command("who").Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(out), "\n")
	users := []string{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
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
		name, _ := p.Name()
		cmdline, _ := p.Cmdline()
		username, _ := p.Username()
		infos = append(infos, ProcessInfo{
			PID:      p.Pid,
			Name:     name,
			Cmdline:  cmdline,
			Username: username,
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

// diffProcesses finds added and removed processes (comparing by PID, Name, Cmdline, Username).
func diffProcesses(current, initial []ProcessInfo) (added, removed []ProcessInfo) {
	initialMap := make(map[string]bool)
	for _, p := range initial {
		key := fmt.Sprintf("%d:%s:%s:%s", p.PID, p.Name, p.Cmdline, p.Username)
		initialMap[key] = true
	}
	for _, p := range current {
		key := fmt.Sprintf("%d:%s:%s:%s", p.PID, p.Name, p.Cmdline, p.Username)
		if !initialMap[key] {
			added = append(added, p)
		}
	}
	currentMap := make(map[string]bool)
	for _, p := range current {
		key := fmt.Sprintf("%d:%s:%s:%s", p.PID, p.Name, p.Cmdline, p.Username)
		currentMap[key] = true
	}
	for _, p := range initial {
		key := fmt.Sprintf("%d:%s:%s:%s", p.PID, p.Name, p.Cmdline, p.Username)
		if !currentMap[key] {
			removed = append(removed, p)
		}
	}
	return
}