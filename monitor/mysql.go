package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/util"
)

// MySQLState holds the persistent state for the MySQL monitor.
type MySQLState struct {
	InitTime         time.Time `json:"initTime"`
	LastUptime       uint64    `json:"lastUptime"`
	LastCheckTime    time.Time `json:"lastCheckTime"`
	LastStopTime     time.Time `json:"lastStopTime"`
	LastDeadlocks    uint64    `json:"lastDeadlocks"`
	LastSlowQueries  uint64    `json:"lastSlowQueries"`
	IsStopped        bool      `json:"isStopped"`
}

const stateFile = ".mysql_init.json"

// loadState loads the MySQL state from the local file.
func loadState() (*MySQLState, error) {
	data, err := os.ReadFile(stateFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		slog.Error("Failed to read MySQL state file", "error", err, "component", "mysql")
		return nil, fmt.Errorf("failed to read MySQL state file: %w", err)
	}
	var state MySQLState
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Error("Failed to unmarshal MySQL state", "error", err, "component", "mysql")
		return nil, fmt.Errorf("failed to unmarshal MySQL state: %w", err)
	}
	return &state, nil
}

// saveState saves the MySQL state to the local file.
func saveState(state *MySQLState) error {
	data, err := json.Marshal(state)
	if err != nil {
		slog.Error("Failed to marshal MySQL state", "error", err, "component", "mysql")
		return fmt.Errorf("failed to marshal MySQL state: %w", err)
	}
	if err := os.WriteFile(stateFile, data, 0644); err != nil {
		slog.Error("Failed to write MySQL state file", "error", err, "component", "mysql")
		return fmt.Errorf("failed to write MySQL state file: %w", err)
	}
	slog.Debug("Saved MySQL state to file", "component", "mysql")
	return nil
}

// getMySQLUptime fetches the Uptime from MySQL in seconds.
func getMySQLUptime(db *sql.DB, ctx context.Context) (uint64, error) {
	var variableName string
	var uptime uint64
	err := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Uptime'").Scan(&variableName, &uptime)
	if err != nil {
		slog.Warn("Failed to query Uptime", "error", err, "component", "mysql")
		return 0, fmt.Errorf("failed to query Uptime: %w", err)
	}
	return uptime, nil
}

// getCurrentDeadlocks fetches the current Innodb_deadlocks count.
func getCurrentDeadlocks(db *sql.DB, ctx context.Context) (uint64, error) {
	var variableName string
	var deadlocks uint64
	err := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Innodb_deadlocks'").Scan(&variableName, &deadlocks)
	if err != nil {
		slog.Warn("Failed to query Innodb_deadlocks", "error", err, "component", "mysql")
		return 0, fmt.Errorf("failed to query Innodb_deadlocks: %w", err)
	}
	return deadlocks, nil
}

// getCurrentSlowQueries fetches the current Slow_queries count.
func getCurrentSlowQueries(db *sql.DB, ctx context.Context) (uint64, error) {
	var variableName string
	var slowQueries uint64
	err := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Slow_queries'").Scan(&variableName, &slowQueries)
	if err != nil {
		slog.Warn("Failed to query Slow_queries", "error", err, "component", "mysql")
		return 0, fmt.Errorf("failed to query Slow_queries: %w", err)
	}
	return slowQueries, nil
}

// MySQL monitors MySQL instance and sends alerts for connectivity, slave status, deadlocks, connections, and slow queries.
func MySQL(ctx context.Context, cfg config.MySQLConfig, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration) error {
	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "mysql")
		hostIP = "unknown"
	}

	// Load state
	state, err := loadState()
	if err != nil {
		return err
	}
	fileExisted := state != nil

	// Initialize details for alert message
	var details strings.Builder
	var variableName string
	hasIssue := false

	// Open database connection
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		slog.Error("Failed to open MySQL connection", "dsn", cfg.DSN, "error", err, "component", "mysql")
		details.WriteString(fmt.Sprintf("无法打开数据库连接: %v", err))
		if fileExisted && !state.IsStopped {
			// Calculate runtime
			var runtimeMin int
			if state.LastStopTime.IsZero() {
				// Database was running at last check
				runtimeSeconds := state.LastUptime + uint64(time.Now().Sub(state.LastCheckTime).Seconds())
				runtimeMin = int(runtimeSeconds / 60)
			} else {
				// Database was stopped at last check
				runtimeMin = int(state.LastStopTime.Sub(state.InitTime).Minutes())
			}
			details.WriteString(fmt.Sprintf("\nMySQL 服务停止，运行了 %d 分钟", runtimeMin))
			state.LastStopTime = time.Now()
			state.IsStopped = true
			if err := saveState(state); err != nil {
				slog.Error("Failed to save state on stop", "error", err, "component", "mysql")
			}
			if bot != nil {
				msg := bot.FormatAlert("数据库告警", "服务停止", details.String(), hostIP, "alert")
				if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "服务停止", details.String(), hostIP, "alert", msg); err != nil {
					return err
				}
			}
		}
		return fmt.Errorf("failed to open MySQL connection: %w", err)
	}
	defer db.Close()

	// Check connectivity
	currentTime := time.Now()
	if err := db.PingContext(ctx); err != nil {
		slog.Error("Failed to ping MySQL", "dsn", cfg.DSN, "error", err, "component", "mysql")
		details.WriteString(fmt.Sprintf("数据库 ping 失败: %v", err))
		if fileExisted && !state.IsStopped {
			// Calculate runtime
			var runtimeMin int
			if state.LastStopTime.IsZero() {
				// Database was running at last check
				runtimeSeconds := state.LastUptime + uint64(currentTime.Sub(state.LastCheckTime).Seconds())
				runtimeMin = int(runtimeSeconds / 60)
			} else {
				// Database was stopped at last check
				runtimeMin = int(state.LastStopTime.Sub(state.InitTime).Minutes())
			}
			details.WriteString(fmt.Sprintf("\nMySQL 服务停止，运行了 %d 分钟", runtimeMin))
			state.LastStopTime = currentTime
			state.IsStopped = true
			if err := saveState(state); err != nil {
				slog.Error("Failed to save state on stop", "error", err, "component", "mysql")
			}
			if bot != nil {
				msg := bot.FormatAlert("数据库告警", "服务停止", details.String(), hostIP, "alert")
				if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "服务停止", details.String(), hostIP, "alert", msg); err != nil {
					return err
				}
			}
		}
		return fmt.Errorf("failed to ping MySQL: %w", err)
	}

	// Connection success
	uptime, err := getMySQLUptime(db, ctx)
	if err != nil {
		uptime = 0
	}
	currentDeadlocks, err = getCurrentDeadlocks(db, ctx)
	if err != nil {
		currentDeadlocks = 0
	}
	currentSlowQueries, err := getCurrentSlowQueries(db, ctx)
	if err != nil {
		currentSlowQueries = 0
	}

	if state == nil {
		// First run
		details.WriteString("连接数据库正常")
		if bot != nil {
			msg := bot.FormatAlert("数据库告警", "首次连接", details.String(), hostIP, "alert")
			if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "首次连接", details.String(), hostIP, "alert", msg); err != nil {
				return err
			}
		}
		state = &MySQLState{
			InitTime:        currentTime,
			LastUptime:      uptime,
			LastCheckTime:   currentTime,
			LastStopTime:    time.Time{},
			LastDeadlocks:   currentDeadlocks,
			LastSlowQueries: currentSlowQueries,
			IsStopped:       false,
		}
		if err := saveState(state); err != nil {
			return err
		}
		slog.Info("MySQL first run: connection successful", "component", "mysql")
		return nil // Skip other checks
	}

	// Not first run
	isRecovery := state.IsStopped
	if isRecovery {
		downtimeMin := int(currentTime.Sub(state.LastStopTime).Minutes())
		details.WriteString(fmt.Sprintf("MySQL 服务恢复正常，停机了 %d 分钟", downtimeMin))
		hasIssue = true
		if bot != nil {
			msg := bot.FormatAlert("数据库告警", "服务恢复", details.String(), hostIP, "alert")
			if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "服务恢复", details.String(), hostIP, "alert", msg); err != nil {
				return err
			}
		}
		// Refresh initialization data on recovery
		state.LastDeadlocks = currentDeadlocks
		state.LastSlowQueries = currentSlowQueries
		state.IsStopped = false
		if err := saveState(state); err != nil {
			return err
		}
	}

	// Update state
	state.LastUptime = uptime
	state.LastCheckTime = currentTime
	state.LastStopTime = time.Time{}
	if err := saveState(state); err != nil {
		return err
	}

	// Perform other checks only if not recovery
	if isRecovery {
		return nil
	}

	// Check slave status
	rows, err := db.QueryContext(ctx, "SHOW SLAVE STATUS")
	if err == nil {
		defer rows.Close()
		if rows.Next() {
			columns, _ := rows.Columns()
			values := make([]interface{}, len(columns))
			valuePtrs := make([]interface{}, len(columns))
			for i := range values {
				var v sql.RawBytes
				valuePtrs[i] = &v
				values[i] = &v
			}
			if err := rows.Scan(valuePtrs...); err != nil {
				slog.Warn("Failed to scan slave status", "error", err, "component", "mysql")
			} else {
				slaveStatus := make(map[string]string)
				for i, col := range columns {
					if raw, ok := values[i].(*sql.RawBytes); ok {
						slaveStatus[col] = string(*raw)
					} else {
						slaveStatus[col] = ""
					}
				}
				issues := []string{}
				if slaveStatus["Slave_IO_Running"] != "Yes" {
					issues = append(issues, "Slave_IO_Running 异常")
				}
				if slaveStatus["Slave_SQL_Running"] != "Yes" {
					issues = append(issues, "Slave_SQL_Running 异常")
				}
				if seconds, err := strconv.ParseInt(slaveStatus["Seconds_Behind_Master"], 10, 64); err == nil && seconds > 0 {
					issues = append(issues, "Seconds_Behind_Master 非零")
				}
				if errno, err := strconv.ParseInt(slaveStatus["Last_Errno"], 10, 64); err == nil && errno != 0 {
					issues = append(issues, "Last_Errno 非零")
				}
				if slaveStatus["Last_Error"] != "" {
					issues = append(issues, "Last_Error 非空")
				}
				if len(issues) > 0 {
					hasIssue = true
					details.WriteString("从库状态异常:\n")
					fmt.Fprintf(&details, "| %s | %s |\n",
						"参数",
						"值",
					)
					fmt.Fprintf(&details, "|%s|%s|\n",
						"------",
						"------",
					)
					fmt.Fprintf(&details, "| %s | %s |\n", "Slave_IO_Running", slaveStatus["Slave_IO_Running"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Slave_SQL_Running", slaveStatus["Slave_SQL_Running"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Seconds_Behind_Master", slaveStatus["Seconds_Behind_Master"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Read_Master_Log_Pos", slaveStatus["Read_Master_Log_Pos"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Last_Errno", slaveStatus["Last_Errno"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Last_Error", slaveStatus["Last_Error"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Master_Log_File", slaveStatus["Master_Log_File"])
					slog.Info("MySQL slave status issue detected", "issues", issues, "component", "mysql")
				}
			}
		} else {
			slog.Debug("No slave status found, likely a main node", "component", "mysql")
		}
	} else {
		slog.Warn("Failed to query slave status", "error", err, "component", "mysql")
	}

	// Check deadlocks
	currentDeadlocks, err := getCurrentDeadlocks(db, ctx)
	if err == nil && currentDeadlocks > state.LastDeadlocks {
		hasIssue = true
		deadlockIncrement := currentDeadlocks - state.LastDeadlocks
		details.WriteString(fmt.Sprintf("检测到新死锁数量: %d\n", deadlockIncrement))
		slog.Info("MySQL new deadlocks detected", "increment", deadlockIncrement, "component", "mysql")
		state.LastDeadlocks = currentDeadlocks
	} else if err != nil {
		slog.Warn("Failed to query Innodb_deadlocks", "error", err, "component", "mysql")
	}

	// Check connections
	var threads int
	err = db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Threads_connected'").Scan(&variableName, &threads)
	if err == nil && threads > cfg.MaxConnections {
		hasIssue = true
		details.WriteString(fmt.Sprintf("当前连接数 %d 超过阈值 %d\n", threads, cfg.MaxConnections))
		slog.Info("MySQL high connections detected", "threads", threads, "threshold", cfg.MaxConnections, "component", "mysql")
	} else if err != nil {
		slog.Warn("Failed to query Threads_connected", "error", err, "component", "mysql")
	}

	// Check slow queries
	var slowQueries uint64
	err = db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Slow_queries'").Scan(&variableName, &slowQueries)
	if err == nil && slowQueries > state.LastSlowQueries {
		hasIssue = true
		slowIncrement := slowQueries - state.LastSlowQueries
		details.WriteString(fmt.Sprintf("检测到新慢查询数量: %d\n", slowIncrement))
		slog.Info("MySQL new slow queries detected", "increment", slowIncrement, "component", "mysql")
		state.LastSlowQueries = slowQueries
	} else if err != nil {
		slog.Warn("Failed to query Slow_queries", "error", err, "component", "mysql")
	}

	// Save updated state
	if err := saveState(state); err != nil {
		return err
	}

	// Send aggregated alert if issues detected
	if hasIssue && details.Len() > 0 {
		if bot != nil {
			msg := bot.FormatAlert("数据库告警", "异常", details.String(), hostIP, "alert")
			if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "异常", details.String(), hostIP, "alert", msg); err != nil {
				return err
			}
		}
	}

	slog.Debug("No MySQL issues detected", "component", "mysql")
	return nil
}

// sendMySQLAlert sends a deduplicated Telegram alert for the MySQL module.
func sendMySQLAlert(ctx context.Context, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, serviceName, eventName, details, hostIP, alertType, message string) error {
	if bot == nil {
		slog.Warn("Alert bot is nil, skipping alert", "service_name", serviceName, "event_name", eventName, "component", "mysql")
		return nil
	}
	hash, err := util.MD5Hash(details)
	if err != nil {
		slog.Error("Failed to generate alert hash", "error", err, "component", "mysql")
		return fmt.Errorf("failed to generate alert hash: %w", err)
	}
	cacheMutex.Lock()
	now := time.Now()
	if timestamp, ok := alertCache[hash]; ok && now.Sub(timestamp) < alertSilenceDuration {
		slog.Info("Skipping duplicate alert", "hash", hash, "component", "mysql")
		cacheMutex.Unlock()
		return nil
	}
	alertCache[hash] = now
	// Clean up old cache entries
	for h, t := range alertCache {
		if now.Sub(t) >= alertSilenceDuration {
			delete(alertCache, h)
			slog.Debug("Removed expired alert cache entry", "hash", h, "component", "mysql")
		}
	}
	cacheMutex.Unlock()
	slog.Debug("Sending alert", "message", message, "details", details, "component", "mysql")
	if err := bot.SendAlert(ctx, serviceName, eventName, details, hostIP, alertType); err != nil {
		slog.Error("Failed to send alert", "error", err, "message", message, "details", details, "component", "mysql")
		return fmt.Errorf("failed to send alert: %w", err)
	}
	slog.Info("Sent alert", "message", message, "details", details, "component", "mysql")
	return nil
}