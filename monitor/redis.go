package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/util"
)

// Redis monitors Redis cluster and sends alerts for connectivity, node failures, slot coverage, and big keys.
func Redis(ctx context.Context, cfg config.RedisConfig, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration) error {
	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "redis")
		hostIP = "unknown"
	}

	// Detect Redis mode (cluster or single-node)
	isCluster, err := isRedisCluster(ctx, cfg)
	if err != nil {
		slog.Error("Failed to detect Redis mode", "addr", cfg.Addr, "error", err, "component", "redis")
		details := fmt.Sprintf("Failed to detect Redis mode: %v", err)
		return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Mode Detection Failed", details, hostIP, "alert", "redis", nil)
	}
	slog.Info("Detected Redis mode", "is_cluster", isCluster, "component", "redis")

	// Initialize Redis client based on mode
	var client redis.UniversalClient
	if isCluster {
		client = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:    []string{cfg.Addr},
			Password: cfg.Password,
		})
	} else {
		client = redis.NewClient(&redis.Options{
			Addr:     cfg.Addr,
			Password: cfg.Password,
			DB:       0, // Default to DB 0, will switch for multi-DB scan
		})
	}
	defer client.Close()

	// Check connectivity with timeout
	pingCtx, pingCancel := context.WithTimeout(ctx, 2*time.Second)
	defer pingCancel()
	pong, err := client.Ping(pingCtx).Result()
	if err != nil || pong != "PONG" {
		slog.Error("Failed to ping Redis", "addr", cfg.Addr, "error", err, "component", "redis")
		details := fmt.Sprintf("Failed to connect to Redis: %v", err)
		return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Connection Failed", details, hostIP, "alert", "redis", nil)
	}

	// Get overall metrics using INFO to monitor memory, CPU, etc.
	infoCtx, infoCancel := context.WithTimeout(ctx, 2*time.Second)
	defer infoCancel()
	info, err := client.Info(infoCtx, "all").Result()
	if err != nil {
		slog.Error("Failed to get Redis INFO", "error", err, "component", "redis")
		details := fmt.Sprintf("Failed to get Redis INFO: %v", err)
		return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "INFO Retrieval Failed", details, hostIP, "alert", "redis", nil)
	}

	// Parse INFO for key metrics
	metrics := parseRedisInfo(info)
	if metrics["used_memory_rss"].(float64) / metrics["used_memory"].(float64) > 1.5 {
		details := fmt.Sprintf("High memory fragmentation: %v", metrics["mem_fragmentation_ratio"])
		slog.Info("High memory fragmentation detected", "ratio", metrics["mem_fragmentation_ratio"], "component", "redis")
		if err := sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "High Memory Fragmentation", details, hostIP, "alert", "redis", nil); err != nil {
			return err
		}
	}
	if metrics["evicted_keys"].(int64) > 0 {
		details := fmt.Sprintf("Keys evicted due to memory pressure: %d", metrics["evicted_keys"])
		slog.Info("Key evictions detected", "evicted_keys", metrics["evicted_keys"], "component", "redis")
		if err := sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Key Evictions", details, hostIP, "alert", "redis", nil); err != nil {
			return err
		}
	}
	hitRatio := float64(metrics["keyspace_hits"].(int64)) / float64(metrics["keyspace_hits"].(int64) + metrics["keyspace_misses"].(int64))
	if hitRatio < 0.9 {
		details := fmt.Sprintf("Low cache hit ratio: %.2f", hitRatio)
		slog.Info("Low cache hit ratio detected", "hit_ratio", hitRatio, "component", "redis")
		if err := sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Low Cache Hit Ratio", details, hostIP, "alert", "redis", nil); err != nil {
			return err
		}
	}

	if isCluster {
		// Check cluster nodes with timeout (cluster mode only)
		nodesCtx, nodesCancel := context.WithTimeout(ctx, 3*time.Second)
		defer nodesCancel()
		nodes, err := client.ClusterNodes(nodesCtx).Result()
		if err != nil {
			slog.Error("Failed to get Redis cluster nodes", "error", err, "component", "redis")
			details := fmt.Sprintf("Failed to get cluster nodes: %v", err)
			return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Node Status Error", details, hostIP, "alert", "redis", nil)
		}

		failedNodes := []string{}
		for _, line := range strings.Split(nodes, "\n") {
			if line == "" {
				continue
			}
			if strings.Contains(line, "fail") {
				fields := strings.Fields(line)
				if len(fields) > 1 {
					failedNodes = append(failedNodes, alert.EscapeMarkdown(fields[1])) // addr
				}
			}
		}
		if len(failedNodes) > 0 {
			details := fmt.Sprintf("Detected failed nodes: %s", strings.Join(failedNodes, ", "))
			slog.Info("Redis node failures detected", "nodes", failedNodes, "component", "redis")
			specificFields := map[string]interface{}{
				"failed_nodes": strings.Join(failedNodes, ", "),
			}
			if err := sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Node Failure", details, hostIP, "alert", "redis", specificFields); err != nil {
				return err
			}
		}

		// Check slot coverage with timeout (cluster mode only)
		slotsCtx, slotsCancel := context.WithTimeout(ctx, 3*time.Second)
		defer slotsCancel()
		slots, err := client.ClusterSlots(slotsCtx).Result()
		if err != nil {
			slog.Error("Failed to get Redis cluster slots", "error", err, "component", "redis")
			details := fmt.Sprintf("Failed to get cluster slots: %v", err)
			return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Slot Coverage Error", details, hostIP, "alert", "redis", nil)
		}
		covered := 0
		for _, slot := range slots {
			covered += int(slot.End - slot.Start + 1)
		}
		if covered != 16384 {
			details := fmt.Sprintf("Incomplete slot coverage: %d/16384", covered)
			slog.Info("Redis slot coverage issue detected", "covered", covered, "component", "redis")
			if err := sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Incomplete Slot Coverage", details, hostIP, "alert", "redis", nil); err != nil {
				return err
			}
		}
	}

	// Check big keys with timeout and sampling to minimize impact
	scanTimeout, err := time.ParseDuration(cfg.ScanTimeout)
	if err != nil {
		scanTimeout = 5 * time.Second // Fallback to 5 seconds
		slog.Warn("Invalid scan_timeout, using default", "scan_timeout", cfg.ScanTimeout, "error", err, "component", "redis")
	}
	scanCtx, scanCancel := context.WithTimeout(ctx, scanTimeout)
	defer scanCancel()

	bigKeys := []string{}
	bigKeysCount := 0
	scannedDBs := []string{}
	const sampleCount = 100 // Number of random keys to sample per DB or cluster

	slog.Info("Sampling for big keys", "threshold_bytes", cfg.BigKeyThreshold, "sample_count", sampleCount, "timeout", scanTimeout.String(), "component", "redis")
	if isCluster {
		// Cluster mode: Sample on DB 0 across all masters
		scannedDBs = append(scannedDBs, "0 (cluster mode)")
		err = sampleBigKeys(scanCtx, client, cfg, sampleCount, &bigKeys, &bigKeysCount)
	} else {
		// Single-node mode: Get number of databases and sample each
		dbCountStr, err := client.ConfigGet(scanCtx, "databases").Result()
		if err != nil {
			slog.Error("Failed to get number of databases", "error", err, "component", "redis")
			details := fmt.Sprintf("Failed to get number of databases: %v", err)
			return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Database Count Error", details, hostIP, "alert", "redis", nil)
		}
		dbCount, err := strconv.Atoi(dbCountStr["databases"])
		if err != nil {
			slog.Error("Invalid databases config", "value", dbCountStr["databases"], "error", err, "component", "redis")
			dbCount = 16 // Default to 16 databases
		}
		for db := 0; db < dbCount; db++ {
			dbClient := redis.NewClient(&redis.Options{
				Addr:     cfg.Addr,
				Password: cfg.Password,
				DB:       db,
			})
			defer dbClient.Close()
			scannedDBs = append(scannedDBs, strconv.Itoa(db))
			if err := sampleBigKeys(scanCtx, dbClient, cfg, sampleCount/dbCount+1, &bigKeys, &bigKeysCount); err != nil && err != context.DeadlineExceeded {
				slog.Error("Failed to sample big keys in DB", "db", db, "error", err, "component", "redis")
				details := fmt.Sprintf("Failed to sample big keys in DB %d: %v", db, err)
				return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Big Key Sample Failed", details, hostIP, "alert", "redis", nil)
			}
		}
	}
	if err != nil && err != context.DeadlineExceeded {
		slog.Error("Failed to sample big keys", "error", err, "component", "redis")
		details := fmt.Sprintf("Failed to sample big keys: %v", err)
		return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Big Key Sample Failed", details, hostIP, "alert", "redis", nil)
	}
	if bigKeysCount > 0 {
		details := fmt.Sprintf("Detected %d potential big keys in DBs [%s] (sample of %d keys):\n%s", bigKeysCount, strings.Join(scannedDBs, ", "), sampleCount, strings.Join(bigKeys, "\n"))
		slog.Info("Redis potential big keys detected", "big_keys_count", bigKeysCount, "scanned_dbs", strings.Join(scannedDBs, ", "), "component", "redis")
		specificFields := map[string]interface{}{
			"big_keys_count": bigKeysCount,
		}
		if err := sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis Monitor", "Big Keys Detected", details, hostIP, "alert", "redis", specificFields); err != nil {
			return err
		}
	}

	slog.Debug("No Redis issues detected", "sampled_keys", sampleCount, "scanned_dbs", strings.Join(scannedDBs, ", "), "component", "redis")
	return nil
}

// isRedisCluster detects if Redis is in cluster mode.
func isRedisCluster(ctx context.Context, cfg config.RedisConfig) (bool, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       0, // Use DB 0 for cluster check
	})
	defer client.Close()

	// Check CLUSTER INFO
	info, err := client.ClusterInfo(ctx).Result()
	if err != nil && strings.Contains(err.Error(), "ERR This instance has cluster support disabled") {
		return false, nil // Single-node mode
	}
	if err != nil {
		return false, fmt.Errorf("failed to get cluster info: %w", err)
	}
	if strings.Contains(info, "cluster_state:ok") {
		return true, nil // Cluster mode
	}
	return false, nil // Single-node mode
}

// sampleBigKeys samples random keys for big key detection on the given client to minimize impact.
func sampleBigKeys(ctx context.Context, client redis.UniversalClient, cfg config.RedisConfig, sampleCount int, bigKeys *[]string, bigKeysCount *int) error {
	startTime := time.Now()
	for i := 0; i < sampleCount; i++ {
		select {
		case <-ctx.Done():
			slog.Warn("Big key sampling timed out", "elapsed", time.Since(startTime).String(), "sampled_keys", i, "component", "redis")
			return ctx.Err()
		default:
			key, err := client.RandomKey(ctx).Result()
			if err != nil {
				if err == redis.Nil {
					continue // No keys or empty DB
				}
				slog.Warn("Failed to get random key", "error", err, "component", "redis")
				continue
			}
			// Get memory usage
			memoryUsage, err := client.MemoryUsage(ctx, key).Result()
			if err != nil {
				slog.Warn("Failed to get memory usage for key", "key", key, "error", err, "component", "redis")
				continue
			}
			if memoryUsage > cfg.BigKeyThreshold {
				*bigKeys = append(*bigKeys, fmt.Sprintf("%s (size: %d bytes)", alert.EscapeMarkdown(key), memoryUsage))
				*bigKeysCount++
			}
			// Log progress every 50 samples
			if (i+1)%50 == 0 {
				elapsed := time.Since(startTime).Seconds()
				rate := float64(i+1) / elapsed
				slog.Info("Big key sampling progress", "sampled_keys", i+1, "big_keys_count", *bigKeysCount, "rate_samples_per_sec", fmt.Sprintf("%.0f", rate), "component", "redis")
			}
		}
	}
	return nil
}

// parseRedisInfo parses the INFO output into a map of metrics.
func parseRedisInfo(info string) map[string]interface{} {
	metrics := make(map[string]interface{})
	lines := strings.Split(info, "\n")
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		valueStr := parts[1]
		if value, err := strconv.ParseInt(valueStr, 10, 64); err == nil {
			metrics[key] = value
		} else if value, err := strconv.ParseFloat(valueStr, 64); err == nil {
			metrics[key] = value
		} else {
			metrics[key] = valueStr
		}
	}
	return metrics
}

// sendRedisAlert sends a deduplicated alert for the Redis module.
func sendRedisAlert(ctx context.Context, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, serviceName, eventName, details, hostIP, alertType, module string, specificFields map[string]interface{}) error {
	hash, err := util.MD5Hash(details)
	if err != nil {
		slog.Error("Failed to generate alert hash", "error", err, "component", "redis")
		return fmt.Errorf("failed to generate alert hash: %w", err)
	}
	cacheMutex.Lock()
	now := time.Now()
	if timestamp, ok := alertCache[hash]; ok && now.Sub(timestamp) < alertSilenceDuration {
		slog.Info("Skipping duplicate alert", "hash", hash, "service_name", serviceName, "event_name", eventName, "module", module, "component", "redis")
		cacheMutex.Unlock()
		return nil
	}
	alertCache[hash] = now
	// Clean up old cache entries
	for h, t := range alertCache {
		if now.Sub(t) >= alertSilenceDuration {
			delete(alertCache, h)
			slog.Debug("Removed expired alert cache entry", "hash", h, "component", "redis")
		}
	}
	cacheMutex.Unlock()

	// Generate Telegram message for logging
	message := ""
	if bot != nil {
		message = bot.FormatAlert(serviceName, eventName, details, hostIP, alertType)
	}

	// Send alert to Telegram and monitor-web
	slog.Debug("Sending alert", "message", message, "service_name", serviceName, "event_name", eventName, "module", module, "component", "redis")
	if err := bot.SendAlert(ctx, serviceName, eventName, details, hostIP, alertType, module, specificFields); err != nil {
		slog.Error("Failed to send alert", "error", err, "service_name", serviceName, "event_name", eventName, "module", module, "component", "redis")
		return fmt.Errorf("failed to send alert: %w", err)
	}
	slog.Info("Sent alert", "message", message, "service_name", serviceName, "event_name", eventName, "module", module, "component", "redis")
	return nil
}