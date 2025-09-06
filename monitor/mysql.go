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

// logToFile logs a message to the local slave status log file.
func logToFile(message string) {
	const filePath = ".mysql_slave_log"
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Error("Failed to open slave log file", "error", err, "component", "mysql")
		return
	}
	defer f.Close()
	if _, err := f.WriteString(message + "\n"); err != nil {
		slog.Error("Failed to write to slave log file", "error", err, "component", "mysql")
	} else {
		slog.Debug("Logged slave status to file", "message", message, "component", "mysql")
	}
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
	hasIssue := false

	// Open database connection
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		slog.Error("Failed to open MySQL connection", "dsn", cfg.DSN, "error", err, "component", "mysql")
		details.WriteString(fmt.Sprintf("监控启动，但数据库连接失败: %v", err))
		if bot != nil {
			msg := bot.FormatAlert("数据库告警", "监控启动异常", details.String(), hostIP, "alert")
			if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "监控启动异常", details.String(), hostIP, "alert", msg); err != nil {
				return err
			}
		}
		return fmt.Errorf("failed to open MySQL connection: %w", err)
	}
	defer db.Close()

	// Check connectivity
	currentTime := time.Now()
	if err := db.PingContext(ctx); err != nil {
		slog.Error("Failed to ping MySQL", "dsn", cfg.DSN, "error", err, "component", "mysql")
		details.WriteString(fmt.Sprintf("监控启动，但数据库连接失败: %v", err))
		if fileExisted && !state.IsStopped {
			// Calculate runtime
			var runtimeMin int
			if state.LastStopTime.IsZero() {
				runtimeSeconds := state.LastUptime + uint64(currentTime.Sub(state.LastCheckTime).Seconds())
				runtimeMin = int(runtimeSeconds / 60)
			} else {
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
		if bot != nil {
			msg := bot.FormatAlert("数据库告警", "监控启动异常", details.String(), hostIP, "alert")
			if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "监控启动异常", details.String(), hostIP, "alert", msg); err != nil {
				return err
			}
		}
		return fmt.Errorf("failed to ping MySQL: %w", err)
	}

	// Connection success
	var uptime, currentDeadlocks, currentSlowQueries uint64
	uptime, err = getMySQLUptime(db, ctx)
	if err != nil {
		slog.Warn("Failed to query uptime, setting to 0", "error", err, "component", "mysql")
		uptime = 0
	}
	currentDeadlocks, err = getCurrentDeadlocks(db, ctx)
	if err != nil {
		slog.Warn("Failed to query deadlocks, setting to 0", "error", err, "component", "mysql")
		currentDeadlocks = 0
	}
	currentSlowQueries, err = getCurrentSlowQueries(db, ctx)
	if err != nil {
		slog.Warn("Failed to query slow queries, setting to 0", "error", err, "component", "mysql")
		currentSlowQueries = 0
	}
	slog.Debug("MySQL connection successful", "component", "mysql")

	if state == nil {
		// First run
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
	} else if state.IsStopped {
		// Recovery
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
				errNoStr := slaveStatus["Last_Errno"]
				lastError := slaveStatus["Last_Error"]
				errNo, parseErr := strconv.Atoi(errNoStr)
				if parseErr != nil {
					slog.Warn("Failed to parse Last_Errno", "value", errNoStr, "error", parseErr, "component", "mysql")
					errNo = -1 // Invalid, treat as issue
				}
				if errNo != 0 && lastError != "" {
					issues = append(issues, fmt.Sprintf("Last_Errno: %d", errNo))
					issues = append(issues, fmt.Sprintf("Last_Error: %s", lastError))
				}
				if len(issues) > 0 {
					hasIssue = true
					details.WriteString("从库状态异常:\n")
					fmt.Fprintf(&details, "| %s | %s |\n", "参数", "值")
					fmt.Fprintf(&details, "|%s|%s|\n", "------", "------")
					fmt.Fprintf(&details, "| %s | %s |\n", "Slave_IO_Running", slaveStatus["Slave_IO_Running"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Slave_SQL_Running", slaveStatus["Slave_SQL_Running"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Seconds_Behind_Master", slaveStatus["Seconds_Behind_Master"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Read_Master_Log_Pos", slaveStatus["Read_Master_Log_Pos"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Last_Errno", slaveStatus["Last_Errno"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Last_Error", slaveStatus["Last_Error"])
					fmt.Fprintf(&details, "| %s | %s |\n", "Master_Log_File", slaveStatus["Master_Log_File"])
					slog.Info("MySQL slave status issue detected", "issues", issues, "component", "mysql")
				} else {
					logToFile(fmt.Sprintf("Slave status normal: Last_Errno=0, Last_Error='', Timestamp=%s", time.Now().Format(time.RFC3339)))
				}
			}
		} else {
			slog.Debug("No slave status found, likely a main node", "component", "mysql")
		}
	} else {
		slog.Warn("Failed to query slave status", "error", err, "component", "mysql")
	}

	// Check deadlocks
	if currentDeadlocks > state.LastDeadlocks {
		deadlockIncrement := currentDeadlocks - state.LastDeadlocks
		if deadlockIncrement > uint64(cfg.DeadlockThreshold) {
			hasIssue = true
			details.WriteString(fmt.Sprintf("检测到新死锁数量: %d (超过阈值 %d)\n", deadlockIncrement, cfg.DeadlockThreshold))
			slog.Info("MySQL new deadlocks detected exceeding threshold", "increment", deadlockIncrement, "threshold", cfg.DeadlockThreshold, "component", "mysql")
		} else {
			slog.Info("MySQL new deadlocks detected but below threshold", "increment", deadlockIncrement, "threshold", cfg.DeadlockThreshold, "component", "mysql")
		}
		state.LastDeadlocks = currentDeadlocks
	}

	// Check connections
	var threads int
	var variableName string
	if err := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Threads_connected'").Scan(&variableName, &threads); err != nil {
		slog.Warn("Failed to query Threads_connected", "error", err, "component", "mysql")
	} else if threads > cfg.MaxConnections {
		hasIssue = true
		details.WriteString(fmt.Sprintf("当前连接数 %d 超过阈值 %d\n", threads, cfg.MaxConnections))
		slog.Info("MySQL high connections detected", "threads", threads, "threshold", cfg.MaxConnections, "component", "mysql")
	}

	// Check slow queries
	if currentSlowQueries > state.LastSlowQueries {
		slowIncrement := currentSlowQueries - state.LastSlowQueries
		if slowIncrement > uint64(cfg.SlowQueryThreshold) {
			hasIssue = true
			details.WriteString(fmt.Sprintf("检测到新慢查询数量: %d (超过阈值 %d)\n", slowIncrement, cfg.SlowQueryThreshold))
			slog.Info("MySQL new slow queries detected exceeding threshold", "increment", slowIncrement, "threshold", cfg.SlowQueryThreshold, "component", "mysql")

			// Query top 5 slowest recent queries
			slowLogRows, err := db.QueryContext(ctx, `
				SELECT sql_text, TIME_TO_SEC(query_time) + MICROSECOND(query_time)/1000000.0 AS query_seconds
				FROM mysql.slow_log
				WHERE start_time >= ?
				ORDER BY query_time DESC
				LIMIT 5`, state.LastCheckTime)
			if err != nil {
				slog.Warn("Failed to query slow_log", "error", err, "component", "mysql")
			} else {
				defer slowLogRows.Close()
				details.WriteString("最耗时的5个慢SQL:\n")
				for slowLogRows.Next() {
					var sqlText string
					var querySeconds float64
					if err := slowLogRows.Scan(&sqlText, &querySeconds); err == nil {
						firstChar := ""
						if len(sqlText) > 0 {
							firstChar = string(sqlText[0])
						}
						details.WriteString(fmt.Sprintf("%s + %.2f seconds\n", firstChar, querySeconds))
					}
				}
				if err := slowLogRows.Err(); err != nil {
					slog.Warn("Error iterating slow_log rows", "error", err, "component", "mysql")
				}
			}
		} else {
			slog.Info("MySQL new slow queries detected but below threshold", "increment", slowIncrement, "threshold", cfg.SlowQueryThreshold, "component", "mysql")
		}
		state.LastSlowQueries = currentSlowQueries
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