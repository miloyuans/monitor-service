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

// MySQL monitors MySQL instance and sends alerts for connectivity, slave status, deadlocks, connections, and slow queries.
func MySQL(ctx context.Context, cfg config.MySQLConfig, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration) error {
	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "mysql")
		hostIP = "unknown"
	}

	// Open database connection
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		slog.Error("Failed to open MySQL connection", "dsn", cfg.DSN, "error", err, "component", "mysql")
		details := fmt.Sprintf("无法打开数据库连接: %v", err)
		if bot != nil {
			msg := bot.FormatAlert("数据库告警", "连接失败", details, hostIP, "alert")
			return sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "连接失败", details, hostIP, "alert", msg)
		}
		return fmt.Errorf("failed to open MySQL connection: %w", err)
	}
	defer db.Close()

	// Check connectivity
	if err := db.PingContext(ctx); err != nil {
		slog.Error("Failed to ping MySQL", "dsn", cfg.DSN, "error", err, "component", "mysql")
		details := fmt.Sprintf("数据库 ping 失败: %v", err)
		if bot != nil {
			msg := bot.FormatAlert("数据库告警", "连接失败", details, hostIP, "alert")
			return sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "连接失败", details, hostIP, "alert", msg)
		}
		return fmt.Errorf("failed to ping MySQL: %w", err)
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
					var details strings.Builder
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
					if bot != nil {
						msg := bot.FormatAlert("数据库告警", "从库状态异常", details.String(), hostIP, "alert")
						if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "从库状态异常", details.String(), hostIP, "alert", msg); err != nil {
							return err
						}
					}
				}
			}
		} else {
			slog.Debug("No slave status found, likely a main node", "component", "mysql")
		}
	} else {
		slog.Warn("Failed to query slave status", "error", err, "component", "mysql")
	}

	// Check deadlocks
	var statusType, trxID, status string
	rows, err = db.QueryContext(ctx, "SHOW ENGINE INNODB STATUS")
	if err == nil {
		defer rows.Close()
		if rows.Next() {
			if err := rows.Scan(&statusType, &trxID, &status); err != nil {
				slog.Warn("Failed to scan InnoDB status", "error", err, "component", "mysql")
			} else if strings.Contains(strings.ToLower(status), "deadlock") {
				details := "检测到数据库死锁"
				slog.Info("MySQL deadlock detected", "component", "mysql")
				if bot != nil {
					msg := bot.FormatAlert("数据库告警", "死锁检测", details, hostIP, "alert")
					if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "死锁检测", details, hostIP, "alert", msg); err != nil {
						return err
					}
				}
			}
		}
	} else {
		slog.Warn("Failed to query InnoDB status", "error", err, "component", "mysql")
	}

	// Check connections
	var threads int
	err = db.QueryRowContext(ctx, "SELECT VARIABLE_VALUE FROM INFORMATION_SCHEMA.GLOBAL_STATUS WHERE VARIABLE_NAME = 'THREADS_CONNECTED'").Scan(&threads)
	if err == nil && threads > cfg.MaxConnections {
		details := fmt.Sprintf("当前连接数 %d 超过阈值 %d", threads, cfg.MaxConnections)
		slog.Info("MySQL high connections detected", "threads", threads, "threshold", cfg.MaxConnections, "component", "mysql")
		if bot != nil {
			msg := bot.FormatAlert("数据库告警", "连接数过高", details, hostIP, "alert")
			if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "连接数过高", details, hostIP, "alert", msg); err != nil {
			return err
		}
	}
} else if err != nil {
	slog.Warn("Failed to query threads connected", "error", err, "component", "mysql")
}

// Check slow queries
var slowQueries uint64
err = db.QueryRowContext(ctx, "SELECT VARIABLE_VALUE FROM INFORMATION_SCHEMA.GLOBAL_STATUS WHERE VARIABLE_NAME = 'SLOW_QUERIES'").Scan(&slowQueries)
if err == nil && slowQueries > 0 {
	details := fmt.Sprintf("检测到慢查询: %d", slowQueries)
	slog.Info("MySQL slow queries detected", "slow_queries", slowQueries, "component", "mysql")
	if bot != nil {
		msg := bot.FormatAlert("数据库告警", "慢查询", details, hostIP, "alert")
		if err := sendMySQLAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "数据库告警", "慢查询", details, hostIP, "alert", msg); err != nil {
			return err
		}
	}
} else if err != nil {
	slog.Warn("Failed to query slow queries", "error", err, "component", "mysql")
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