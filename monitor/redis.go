package monitor

import (
	"context"
	"fmt"
	"log/slog"
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

	// Initialize Redis cluster client
	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:    []string{cfg.Addr},
		Password: cfg.Password,
	})
	defer client.Close()

	// Check connectivity
	pong, err := client.Ping(ctx).Result()
	if err != nil || pong != "PONG" {
		slog.Error("Failed to ping Redis", "addr", cfg.Addr, "error", err, "component", "redis")
		details := fmt.Sprintf("无法连接到 Redis: %v", err)
		msg := bot.FormatAlert("Redis告警", "连接失败", details, hostIP, "alert")
		return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis告警", "连接失败", details, hostIP, "alert", msg)
	}

	// Check cluster nodes
	nodes, err := client.ClusterNodes(ctx).Result()
	if err != nil {
		slog.Error("Failed to get Redis cluster nodes", "error", err, "component", "redis")
		details := fmt.Sprintf("无法获取集群节点信息: %v", err)
		msg := bot.FormatAlert("Redis告警", "节点状态异常", details, hostIP, "alert")
		return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis告警", "节点状态异常", details, hostIP, "alert", msg)
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
		details := fmt.Sprintf("检测到失败的节点: %s", strings.Join(failedNodes, ", "))
		slog.Info("Redis node failures detected", "nodes", failedNodes, "component", "redis")
		msg := bot.FormatAlert("Redis告警", "节点失败", details, hostIP, "alert")
		if err := sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis告警", "节点失败", details, hostIP, "alert", msg); err != nil {
			return err
		}
	}

	// Check slot coverage
	slots, err := client.ClusterSlots(ctx).Result()
	if err != nil {
		slog.Error("Failed to get Redis cluster slots", "error", err, "component", "redis")
		details := fmt.Sprintf("无法获取集群槽信息: %v", err)
		msg := bot.FormatAlert("Redis告警", "槽覆盖异常", details, hostIP, "alert")
		return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis告警", "槽覆盖异常", details, hostIP, "alert", msg)
	}
	covered := 0
	for _, slot := range slots {
		covered += int(slot.End - slot.Start + 1)
	}
	if covered != 16384 {
		details := fmt.Sprintf("集群槽覆盖不完整: %d/16384", covered)
		slog.Info("Redis slot coverage issue detected", "covered", covered, "component", "redis")
		msg := bot.FormatAlert("Redis告警", "槽覆盖不足", details, hostIP, "alert")
		if err := sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis告警", "槽覆盖不足", details, hostIP, "alert", msg); err != nil {
			return err
		}
	}

	// Check big keys
	bigKeys := []string{}
	err = client.ForEachMaster(ctx, func(ctx context.Context, master *redis.Client) error {
		var cursor uint64
		for {
			keys, nextCursor, err := master.Scan(ctx, cursor, "*", 100).Result()
			if err != nil {
				slog.Error("Failed to scan Redis keys", "error", err, "component", "redis")
				return fmt.Errorf("failed to scan keys: %w", err)
			}
			for _, key := range keys {
				size, err := master.MemoryUsage(ctx, key).Result()
				if err == nil && size > cfg.BigKeyThreshold {
					bigKeys = append(bigKeys, fmt.Sprintf("%s (size: %d bytes)", alert.EscapeMarkdown(key), size))
				} else if err != nil {
					slog.Warn("Failed to get memory usage for key", "key", key, "error", err, "component", "redis")
				}
			}
			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}
		return nil
	})
	if err != nil {
		details := fmt.Sprintf("无法扫描大键: %v", err)
		slog.Error("Failed to scan big keys", "error", err, "component", "redis")
		msg := bot.FormatAlert("Redis告警", "大键扫描失败", details, hostIP, "alert")
		return sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis告警", "大键扫描失败", details, hostIP, "alert", msg)
	}
	if len(bigKeys) > 0 {
		details := fmt.Sprintf("发现大键:\n%s", strings.Join(bigKeys, "\n"))
		slog.Info("Redis big keys detected", "big_keys", len(bigKeys), "component", "redis")
		msg := bot.FormatAlert("Redis告警", "大键检测", details, hostIP, "alert")
		if err := sendRedisAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Redis告警", "大键检测", details, hostIP, "alert", msg); err != nil {
			return err
		}
	}

	slog.Debug("No Redis issues detected", "component", "redis")
	return nil
}

// sendRedisAlert sends a deduplicated Telegram alert for the Redis module.
func sendRedisAlert(ctx context.Context, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, serviceName, eventName, details, hostIP, alertType, message string) error {
	hash, err := util.MD5Hash(details)
	if err != nil {
		slog.Error("Failed to generate alert hash", "error", err, "component", "redis")
		return fmt.Errorf("failed to generate alert hash: %w", err)
	}
	cacheMutex.Lock()
	now := time.Now()
	if timestamp, ok := alertCache[hash]; ok && now.Sub(timestamp) < alertSilenceDuration {
		slog.Info("Skipping duplicate alert", "hash", hash, "component", "redis")
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
	slog.Debug("Sending alert", "message", message, "component", "redis")
	if err := bot.SendAlert(ctx, serviceName, eventName, details, hostIP, alertType); err != nil {
		slog.Error("Failed to send alert", "error", err, "component", "redis")
		return fmt.Errorf("failed to send alert: %w", err)
	}
	slog.Info("Sent alert", "message", message, "component", "redis")
	return nil
}