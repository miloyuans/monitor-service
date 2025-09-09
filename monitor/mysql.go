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
		slog.Error("Failed to read MySQL state file", "file", stateFile, "error", err, "component", "mysql")
		return nil, fmt.Errorf("failed to read MySQL state file: %w", err)
	}
	var state MySQLState
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Error("Failed to unmarshal MySQL state", "file", stateFile, "error", err, "component", "mysql")
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
		slog.Error("Failed to write MySQL state file", "file", stateFile, "error", err, "component", "mysql")
		return fmt.Errorf("failed to write MySQL state file: %w", err)
	}
	slog.Debug("Saved MySQL state", "file", stateFile, "component", "mysql")
	return nil
}

// getMySQLUptime fetches the Uptime from MySQL in seconds.
func getMySQLUptime(db *sql.DB, ctx context.Context) (uint64, error) {
	var variableName string
	var uptimeStr string
	err := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Uptime'").Scan(&variableName, &uptimeStr)
	if err != nil {
		slog.Warn("Failed to query Uptime", "error", err, "component", "mysql")
		return 0, fmt.Errorf("failed to query Uptime: %w", err)
	}
	uptime, err := strconv.ParseUint(uptimeStr, 10, 64)
	if err != nil {
		slog.Warn("Failed to parse Uptime", "value", uptimeStr, "error", err, "component", "mysql")
		return 0, fmt.Errorf("failed to parse Uptime: %w", err)
	}
	return uptime, nil
}

// getCurrentDeadlocks fetches the current Innodb_deadlocks count.
func getCurrentDeadlocks(db *sql.DB, ctx context.Context) (uint64, error) {
	var variableName string
	var deadlocksStr string
	err := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Innodb_deadlocks'").Scan(&variableName, &deadlocksStr)
	if err != nil {
		slog.Warn("Failed to query Innodb_deadlocks", "error", err, "component", "mysql")
		return 0, fmt.Errorf("failed to query Innodb_deadlocks: %w", err)
	}
	deadlocks, err := strconv.ParseUint(deadlocksStr, 10, 64)
	if err != nil {
		slog.Warn("Failed to parse Innodb_deadlocks", "value", deadlocksStr, "error", err, "component", "mysql")
		return 0, fmt.Errorf("failed to parse Innodb_deadlocks: %w", err)
	}
	return deadlocks, nil
}

// getCurrentSlowQueries fetches the current Slow_queries count.
func getCurrentSlowQueries(db *sql.DB, ctx context.Context) (uint64, error) {
	var variableName string
	var slowQueriesStr string
	err := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Slow_queries'").Scan(&variableName, &slowQueriesStr)
	if err != nil {
		slog.Warn("Failed to query Slow_queries", "error", err, "component", "mysql")
		return 0, fmt.Errorf("failed to query Slow_queries: %w", err)
	}
	slowQueries, err := strconv.ParseUint(slowQueriesStr, 10, 64)
	if err != nil {
		slog.Warn("Failed to parse Slow_queries", "value", slowQueriesStr, "error", err, "component", "mysql")
		return 0, fmt.Errorf("failed to parse Slow_queries: %w", err)
	}
	return slowQueries, nil
}

// logToFile logs a message to the local slave status log file.
func logToFile(message string) {
	const filePath = ".mysql_slave_log"
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Error("Failed to open slave log file", "file", filePath, "error", err, "component", "mysql")
		return
	}
	defer f.Close()
	if _, err := f.WriteString(message + "\n"); err != nil {
		slog.Error("Failed to write to slave log file", "file", filePath, "error", err, "component", "mysql")
	} else {
		slog.Debug("Logged slave status", "file", filePath, "message", message, "component", "mysql")
	}
}

// handleError centralizes error handling and alert sending.
func handleError(ctx context.Context, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, serviceName, eventName, details, hostIP, alertType string) error {
	if bot != nil {
		if err := util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, serviceName, eventName, details, hostIP, alertType, "", map[string]interface{}{}); err != nil {
			return fmt.Errorf("%s: %w", details, err)
		}
	}
	return fmt.Errorf("%s", details)
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
		return handleError(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "监控启动异常", fmt.Sprintf("Failed to load state: %v", err), hostIP, "alert")
	}
	fileExisted := state != nil

	// Open database connection
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		slog.Error("Failed to open MySQL connection", "dsn", cfg.DSN, "error", err, "component", "mysql")
		return handleError(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "监控启动异常", fmt.Sprintf("Failed to open MySQL connection: %v", err), hostIP, "alert")
	}
	defer db.Close()

	// Check connectivity
	currentTime := time.Now()
	if err := db.PingContext(ctx); err != nil {
		slog.Error("Failed to ping MySQL", "dsn", cfg.DSN, "error", err, "component", "mysql")
		details := fmt.Sprintf("监控启动，但数据库连接失败: %v", err)
		if fileExisted && !state.IsStopped {
			var runtimeMin int
			if state.LastStopTime.IsZero() {
				runtimeSeconds := state.LastUptime + uint64(currentTime.Sub(state.LastCheckTime).Seconds())
				runtimeMin = int(runtimeSeconds / 60)
			} else {
				runtimeMin = int(state.LastStopTime.Sub(state.InitTime).Minutes())
			}
			details += fmt.Sprintf("\nMySQL 服务停止，运行了 %d 分钟", runtimeMin)
			state.LastStopTime = currentTime
			state.IsStopped = true
			if err := saveState(state); err != nil {
				slog.Error("Failed to save state on stop", "error", err, "component", "mysql")
			}
			return handleError(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "服务停止", details, hostIP, "alert")
		}
		return handleError(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "监控启动异常", details, hostIP, "alert")
	}

	// Fetch metrics
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
	slog.Debug("MySQL connection successful", "uptime", uptime, "component", "mysql")

	// Initialize or update state
	var details strings.Builder
	hasIssue := false
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
			return handleError(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "状态保存失败", fmt.Sprintf("Failed to save initial state: %v", err), hostIP, "alert")
		}
		slog.Info("MySQL first run: connection successful", "component", "mysql")
	} else if state.IsStopped {
		// Recovery
		downtimeMin := int(currentTime.Sub(state.LastStopTime).Minutes())
		details.WriteString(fmt.Sprintf("MySQL 服务恢复正常，停机了 %d 分钟", downtimeMin))
		hasIssue = true
		state.LastDeadlocks = currentDeadlocks
		state.LastSlowQueries = currentSlowQueries
		state.IsStopped = false
		state.InitTime = currentTime
		state.LastUptime = uptime
		state.LastCheckTime = currentTime
		state.LastStopTime = time.Time{}
		if err := saveState(state); err != nil {
			return handleError(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "状态保存失败", fmt.Sprintf("Failed to save recovery state: %v", err), hostIP, "alert")
		}
	} else if uptime < state.LastUptime {
		// Restart detected
		runtimeMin := int(state.LastUptime / 60)
		details.WriteString(fmt.Sprintf("MySQL 服务重启，之前运行了 %d 分钟", runtimeMin))
		hasIssue = true
		state.InitTime = currentTime
		state.LastDeadlocks = currentDeadlocks
		state.LastSlowQueries = currentSlowQueries
		state.LastUptime = uptime
		state.LastCheckTime = currentTime
		if err := saveState(state); err != nil {
			return handleError(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "状态保存失败", fmt.Sprintf("Failed to save restart state: %v", err), hostIP, "alert")
		}
	} else {
		state.LastUptime = uptime
		state.LastCheckTime = currentTime
		if err := saveState(state); err != nil {
			return handleError(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "状态保存失败", fmt.Sprintf("Failed to save state: %v", err), hostIP, "alert")
		}
	}

	// Check replica status
	if cfg.IsSlave {
		rows, err := db.QueryContext(ctx, "SHOW SLAVE STATUS")
		if err != nil {
			slog.Warn("Failed to query replica status", "error", err, "component", "mysql")
			details.WriteString(fmt.Sprintf("无法查询从库状态: %v\n", err))
			hasIssue = true
		} else {
			defer rows.Close()
			if rows.Next() {
				columns, err := rows.Columns()
				if err != nil {
					slog.Warn("Failed to get replica status columns", "error", err, "component", "mysql")
					details.WriteString(fmt.Sprintf("无法获取从库状态列: %v\n", err))
					hasIssue = true
				} else {
					values := make([]interface{}, len(columns))
					for i := range values {
						var v sql.RawBytes
						values[i] = &v
					}
					if err := rows.Scan(values...); err != nil {
						slog.Warn("Failed to scan replica status", "error", err, "component", "mysql")
						details.WriteString(fmt.Sprintf("无法扫描从库状态: %v\n", err))
						hasIssue = true
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
						if secondsStr := slaveStatus["Seconds_Behind_Master"]; secondsStr != "" {
							if seconds, err := strconv.ParseInt(secondsStr, 10, 64); err == nil && seconds > int64(cfg.SecondsBehindThreshold) {
								issues = append(issues, fmt.Sprintf("Seconds_Behind_Master: %d (超过阈值 %d)", seconds, cfg.SecondsBehindThreshold))
							} else if err != nil {
								slog.Warn("Failed to parse Seconds_Behind_Master", "value", secondsStr, "error", err, "component", "mysql")
								issues = append(issues, fmt.Sprintf("Seconds_Behind_Master: 无效值 %s", secondsStr))
							}
						}
						if errNoStr := slaveStatus["Last_Errno"]; errNoStr != "" {
							if errNo, err := strconv.ParseInt(errNoStr, 10, 64); err == nil && errNo != 0 {
								issues = append(issues, fmt.Sprintf("Last_Errno: %d", errNo))
								if lastError := slaveStatus["Last_Error"]; lastError != "" {
									issues = append(issues, fmt.Sprintf("Last_Error: %s", lastError))
								}
							} else if err != nil {
								slog.Warn("Failed to parse Last_Errno", "value", errNoStr, "error", err, "component", "mysql")
								issues = append(issues, fmt.Sprintf("Last_Errno: 无效值 %s", errNoStr))
							}
						}
						if len(issues) > 0 {
							hasIssue = true
							details.WriteString("从库状态异常:\n")
							fmt.Fprintf(&details, "| %s | %s |\n", "参数", "值")
							fmt.Fprintf(&details, "|%s|%s|\n", "------", "------")
							for _, key := range []string{"Slave_IO_Running", "Slave_SQL_Running", "Seconds_Behind_Master", "Read_Master_Log_Pos", "Last_Errno", "Last_Error", "Master_Log_File"} {
								fmt.Fprintf(&details, "| %s | %s |\n", key, slaveStatus[key])
							}
							slog.Info("MySQL replica status issue detected", "issues", issues, "component", "mysql")
						} else {
							logToFile(fmt.Sprintf("Replica status normal: Last_Errno=0, Last_Error='', Timestamp=%s", time.Now().Format(time.RFC3339)))
						}
					}
				}
			} else {
				slog.Debug("No replica status found, likely a primary node", "component", "mysql")
			}
		}
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

			// Query up to 5 slowest recent queries
			slowLogRows, err := db.QueryContext(ctx, `
				SELECT sql_text, TIME_TO_SEC(query_time) + MICROSECOND(query_time)/1000000.0 AS query_seconds
				FROM mysql.slow_log
				WHERE start_time >= ?
				ORDER BY query_time DESC
				LIMIT 5`, state.LastCheckTime)
			if err != nil {
				slog.Warn("Failed to query slow_log", "error", err, "component", "mysql")
				details.WriteString("无法查询慢查询日志: 错误\n")
			} else {
				defer slowLogRows.Close()
				slowQueries := []string{}
				for slowLogRows.Next() {
					var sqlText string
					var querySeconds float64
					if err := slowLogRows.Scan(&sqlText, &querySeconds); err == nil {
						firstChar := ""
						if len(sqlText) > 0 {
							firstChar = string(sqlText[0])
						}
						slowQueries = append(slowQueries, fmt.Sprintf("%s + %.2f seconds", firstChar, querySeconds))
					}
				}
				if err := slowLogRows.Err(); err != nil {
					slog.Warn("Error iterating slow_log rows", "error", err, "component", "mysql")
					details.WriteString("迭代慢查询日志时出错: 错误\n")
				}
				if len(slowQueries) > 0 {
					details.WriteString(fmt.Sprintf("最耗时的%d个慢SQL:\n", len(slowQueries)))
					for _, query := range slowQueries {
						details.WriteString(fmt.Sprintf("%s\n", query))
					}
				} else {
					details.WriteString("未找到慢查询记录\n")
				}
			}
		} else {
			slog.Info("MySQL new slow queries detected but below threshold", "increment", slowIncrement, "threshold", cfg.SlowQueryThreshold, "component", "mysql")
		}
		state.LastSlowQueries = currentSlowQueries
	}

	// Save updated state
	if err := saveState(state); err != nil {
		return handleError(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "状态保存失败", fmt.Sprintf("Failed to save state: %v", err), hostIP, "alert")
	}

	// Send aggregated alert if issues detected
	if hasIssue && details.Len() > 0 {
		return handleError(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "异常", details.String(), hostIP, "alert")
	}

	slog.Debug("No MySQL issues detected", "component", "mysql")
	return nil
}