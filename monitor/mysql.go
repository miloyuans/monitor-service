package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/util"
)

var (
	isRunning        = false
	isFirstRun       = true
	lastStartTime    time.Time
	lastStopTime     time.Time
	lastSlowQueries  uint64
	lastDeadlocks    uint64
)

// MySQL monitors MySQL instance and sends alerts for connectivity, slave status, deadlocks, connections, and slow queries.
func MySQL(ctx context.Context, cfg config.MySQLConfig, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration) error {
	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "mysql")
		hostIP = "unknown"
	}

	// Initialize details for alert message
	var details strings.Builder
	hasIssue := false

	// Open database connection
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		slog.Error("Failed to open MySQL connection", "dsn", cfg.DSN, "error", err, "component", "mysql")
		details.WriteString(fmt.Sprintf("无法打开数据库连接: %v", err))
		if bot != nil {
			msg := bot.FormatAlert("数据库告警", "连接失败", details.String(), hostIP, "alert")
			if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "连接失败", details.String(), hostIP, "alert", msg); err != nil {
				return err
			}
		}
		isFirstRun = false // Mark first run complete even on failure
		return fmt.Errorf("failed to open MySQL connection: %w", err)
	}
	defer db.Close()

	// Check connectivity
	if err := db.PingContext(ctx); err != nil {
		slog.Error("Failed to ping MySQL", "dsn", cfg.DSN, "error", err, "component", "mysql")
		if isRunning {
			isRunning = false
			currentTime := time.Now()
			lastStopTime = currentTime
			if !lastStartTime.IsZero() {
				runtimeMin := int(currentTime.Sub(lastStartTime).Minutes())
				details.WriteString(fmt.Sprintf("MySQL 服务停止，运行了 %d 分钟", runtimeMin))
			} else {
				details.WriteString("MySQL 服务停止")
			}
			if bot != nil {
				msg := bot.FormatAlert("数据库告警", "服务停止", details.String(), hostIP, "alert")
				if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "服务停止", details.String(), hostIP, "alert", msg); err != nil {
					return err
				}
			}
		}
		isFirstRun = false // Mark first run complete
		return fmt.Errorf("failed to ping MySQL: %w", err)
	}

	// Handle first run
	if isFirstRun {
		isRunning = true
		lastStartTime = time.Now()
		isFirstRun = false
		if bot != nil {
			details.WriteString("连接数据库正常")
			msg := bot.FormatAlert("数据库告警", "首次连接", details.String(), hostIP, "alert")
			if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "首次连接", details.String(), hostIP, "alert", msg); err != nil {
				return err
			}
		}
		slog.Info("MySQL first run: connection successful", "component", "mysql")
		return nil
	}

	// Handle subsequent runs
	if !isRunning {
		isRunning = true
		currentTime := time.Now()
		if !lastStopTime.IsZero() {
			downtimeMin := int(currentTime.Sub(lastStopTime).Minutes())
			details.WriteString(fmt.Sprintf("MySQL 服务恢复正常，停机了 %d 分钟\n", downtimeMin))
			hasIssue = true
		} else {
			details.WriteString("MySQL 服务恢复正常\n")
			hasIssue = true
		}
		lastStartTime = currentTime
		lastStopTime = time.Time{}
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
	var currentDeadlocks uint64
	err = db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Innodb_deadlocks'").Scan(&currentDeadlocks)
	if err != nil {
		slog.Warn("Failed to query Innodb_deadlocks", "error", err, "component", "mysql")
	} else if currentDeadlocks > lastDeadlocks {
		hasIssue = true
		deadlockIncrement := currentDeadlocks - lastDeadlocks
		details.WriteString(fmt.Sprintf("检测到新死锁数量: %d\n", deadlockIncrement))
		slog.Info("MySQL new deadlocks detected", "increment", deadlockIncrement, "component", "mysql")
		lastDeadlocks = currentDeadlocks
	}

	// Check connections
	var threads int
	err = db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Threads_connected'").Scan(&threads)
	if err == nil && threads > cfg.MaxConnections {
		hasIssue = true
		details.WriteString(fmt.Sprintf("当前连接数 %d 超过阈值 %d\n", threads, cfg.MaxConnections))
		slog.Info("MySQL high connections detected", "threads", threads, "threshold", cfg.MaxConnections, "component", "mysql")
	} else if err != nil {
		slog.Warn("Failed to query Threads_connected", "error", err, "component", "mysql")
	}

	// Check slow queries
	var currentSlowQueries uint64
	err = db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Slow_queries'").Scan(&currentSlowQueries)
	if err != nil {
		slog.Warn("Failed to query Slow_queries", "error", err, "component", "mysql")
	} else if currentSlowQueries > lastSlowQueries {
		hasIssue = true
		slowIncrement := currentSlowQueries - lastSlowQueries
		details.WriteString(fmt.Sprintf("检测到新慢查询数量: %d\n", slowIncrement))
		slog.Info("MySQL new slow queries detected", "increment", slowIncrement, "component", "mysql")
		lastSlowQueries = currentSlowQueries
	}

	// Send alert for recovery or other issues (slave status, deadlocks, connections, slow queries)
	if hasIssue && details.Len() > 0 {
		if bot != nil {
			eventName := "异常"
			if strings.Contains(details.String(), "MySQL 服务恢复正常") {
				eventName = "服务恢复"
			}
			msg := bot.FormatAlert("数据库告警", eventName, details.String(), hostIP, "alert")
			if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", eventName, details.String(), hostIP, "alert", msg); err != nil {
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