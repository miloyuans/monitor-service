package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"monitor-service/alert"
	"monitor-service/config"
)

// MySQL checks MySQL for connectivity, slave status, deadlocks, connections, and slow queries.
func MySQL(ctx context.Context, cfg config.MySQLConfig, bot *alert.AlertBot) ([]string, string, error) {
	var hostIP string // Placeholder for host IP, to be implemented if needed
	var messages []string

	// Open database connection
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		slog.Error("Failed to open MySQL connection", "dsn", cfg.DSN, "error", err, "component", "mysql")
		msg := bot.FormatAlert(
			fmt.Sprintf("MySQL (%s)", cfg.ClusterName),
			"连接失败",
			fmt.Sprintf("无法打开数据库连接: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to open MySQL connection: %w", err)
	}
	defer db.Close()

	// Check connectivity
	if err := db.PingContext(ctx); err != nil {
		slog.Error("Failed to ping MySQL", "dsn", cfg.DSN, "error", err, "component", "mysql")
		msg := bot.FormatAlert(
			fmt.Sprintf("MySQL (%s)", cfg.ClusterName),
			"连接失败",
			fmt.Sprintf("数据库 ping 失败: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to ping MySQL: %w", err)
	}

	// Check slave status
	rows, err := db.QueryContext(ctx, "SHOW SLAVE STATUS")
	if err == nil {
		defer rows.Close()
		if rows.Next() {
			columns, _ := rows.Columns()
			values := make([]interface{}, len(columns))
			for i := range values {
				var v sql.RawBytes
				values[i] = &v
			}
			if err := rows.Scan(values...); err != nil {
				slog.Warn("Failed to scan slave status", "error", err, "component", "mysql")
			} else {
				slaveIO := string(*values[10].(*sql.RawBytes))  // Slave_IO_Running
				slaveSQL := string(*values[11].(*sql.RawBytes)) // Slave_SQL_Running
				if slaveIO != "Yes" || slaveSQL != "Yes" {
					msg := bot.FormatAlert(
						fmt.Sprintf("MySQL (%s)", cfg.ClusterName),
						"从库状态异常",
						fmt.Sprintf("从库未正常运行: Slave_IO_Running=%s, Slave_SQL_Running=%s", slaveIO, slaveSQL),
						hostIP,
						"alert",
					)
					messages = append(messages, msg)
				}
			}
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
				msg := bot.FormatAlert(
					fmt.Sprintf("MySQL (%s)", cfg.ClusterName),
					"死锁检测",
					"检测到数据库死锁",
					hostIP,
					"alert",
				)
				messages = append(messages, msg)
			}
		}
	} else {
		slog.Warn("Failed to query InnoDB status", "error", err, "component", "mysql")
	}

	// Check connections
	var threads int
	err = db.QueryRowContext(ctx, "SELECT VARIABLE_VALUE FROM INFORMATION_SCHEMA.GLOBAL_STATUS WHERE VARIABLE_NAME = 'THREADS_CONNECTED'").Scan(&threads)
	if err == nil && threads > cfg.MaxConnections {
		msg := bot.FormatAlert(
			fmt.Sprintf("MySQL (%s)", cfg.ClusterName),
			"连接数过高",
			fmt.Sprintf("当前连接数 %d 超过阈值 %d", threads, cfg.MaxConnections),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	} else if err != nil {
		slog.Warn("Failed to query threads connected", "error", err, "component", "mysql")
	}

	// Check slow queries
	var slowQueries uint64
	err = db.QueryRowContext(ctx, "SELECT VARIABLE_VALUE FROM INFORMATION_SCHEMA.GLOBAL_STATUS WHERE VARIABLE_NAME = 'SLOW_QUERIES'").Scan(&slowQueries)
	if err == nil && slowQueries > 0 {
		msg := bot.FormatAlert(
			fmt.Sprintf("MySQL (%s)", cfg.ClusterName),
			"慢查询",
			fmt.Sprintf("检测到慢查询: %d", slowQueries),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	} else if err != nil {
		slog.Warn("Failed to query slow queries", "error", err, "component", "mysql")
	}

	if len(messages) > 0 {
		return messages, hostIP, fmt.Errorf("MySQL issues detected")
	}
	return nil, hostIP, nil
}