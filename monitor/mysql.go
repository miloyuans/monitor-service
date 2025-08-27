package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"monitor-service/config"
)

// MySQL checks MySQL for connectivity, slave status, deadlocks, connections, and slow queries.
func MySQL(ctx context.Context, cfg config.MySQLConfig, clusterName string) ([]string, error) {
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return []string{fmt.Sprintf("**MySQL (%s)**: Open failed: %v", clusterName, err)}, err
	}
	defer db.Close()

	clusterPrefix := fmt.Sprintf("**MySQL (%s)**", clusterName)
	msgs := []string{}

	if err := db.PingContext(ctx); err != nil {
		return []string{fmt.Sprintf("%s: Ping failed: %v", clusterPrefix, err)}, err
	}

	// Check slave status.
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
			rows.Scan(values...)
			slaveIO := string(*values[10].(*sql.RawBytes))  // Slave_IO_Running
			slaveSQL := string(*values[11].(*sql.RawBytes)) // Slave_SQL_Running
			if slaveIO != "Yes" || slaveSQL != "Yes" {
				msgs = append(msgs, fmt.Sprintf("%s: Slave not running: IO=%s, SQL=%s", clusterPrefix, slaveIO, slaveSQL))
			}
		}
	}

	// Check deadlocks.
	var statusType, trxId, status string
	rows, err = db.QueryContext(ctx, "SHOW ENGINE INNODB STATUS")
	if err == nil {
		defer rows.Close()
		if rows.Next() {
			rows.Scan(&statusType, &trxId, &status)
			if strings.Contains(status, "deadlock") {
				msgs = append(msgs, fmt.Sprintf("%s: Deadlock detected", clusterPrefix))
			}
		}
	}

	// Check connections.
	var threads int
	err = db.QueryRowContext(ctx, "SELECT VARIABLE_VALUE FROM INFORMATION_SCHEMA.GLOBAL_STATUS WHERE VARIABLE_NAME = 'THREADS_CONNECTED'").Scan(&threads)
	if err == nil && threads > cfg.MaxConnections {
		msgs = append(msgs, fmt.Sprintf("%s: High connections: %d > %d", clusterPrefix, threads, cfg.MaxConnections))
	}

	// Check slow queries.
	var slowQueries uint64
	err = db.QueryRowContext(ctx, "SELECT VARIABLE_VALUE FROM INFORMATION_SCHEMA.GLOBAL_STATUS WHERE VARIABLE_NAME = 'SLOW_QUERIES'").Scan(&slowQueries)
	if err == nil && slowQueries > 0 {
		msgs = append(msgs, fmt.Sprintf("%s: Slow queries: %d", clusterPrefix, slowQueries))
	}

	if len(msgs) > 0 {
		return msgs, fmt.Errorf("mysql issues")
	}
	return nil, nil
}