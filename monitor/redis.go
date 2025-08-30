package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/redis/go-redis/v9"
	"monitor-service/alert"
	"monitor-service/config"
)

// Redis checks the Redis cluster for connectivity, node failures, slot coverage, and big keys.
func Redis(ctx context.Context, cfg config.RedisConfig, bot *alert.AlertBot) ([]string, string, error) {
	var hostIP string // Placeholder for host IP, to be implemented if needed
	var messages []string

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
		msg := bot.FormatAlert(
			fmt.Sprintf("Redis (%s)", cfg.ClusterName),
			"连接失败",
			fmt.Sprintf("无法连接到 Redis: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to ping Redis: %w", err)
	}

	// Check cluster nodes
	nodes, err := client.ClusterNodes(ctx).Result()
	if err != nil {
		slog.Error("Failed to get Redis cluster nodes", "error", err, "component", "redis")
		msg := bot.FormatAlert(
			fmt.Sprintf("Redis (%s)", cfg.ClusterName),
			"节点状态异常",
			fmt.Sprintf("无法获取集群节点信息: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to get cluster nodes: %w", err)
	}

	failedNodes := []string{}
	for _, line := range strings.Split(nodes, "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "fail") {
			fields := strings.Fields(line)
			if len(fields) > 1 {
				failedNodes = append(failedNodes, fields[1]) // addr
			}
		}
	}
	if len(failedNodes) > 0 {
		msg := bot.FormatAlert(
			fmt.Sprintf("Redis (%s)", cfg.ClusterName),
			"节点失败",
			fmt.Sprintf("检测到失败的节点: %s", strings.Join(failedNodes, ", ")),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	}

	// Check slot coverage
	slots, err := client.ClusterSlots(ctx).Result()
	if err != nil {
		slog.Error("Failed to get Redis cluster slots", "error", err, "component", "redis")
		msg := bot.FormatAlert(
			fmt.Sprintf("Redis (%s)", cfg.ClusterName),
			"槽覆盖异常",
			fmt.Sprintf("无法获取集群槽信息: %v", err),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	} else {
		covered := 0
		for _, slot := range slots {
			covered += int(slot.End - slot.Start + 1)
		}
		if covered != 16384 {
			msg := bot.FormatAlert(
				fmt.Sprintf("Redis (%s)", cfg.ClusterName),
				"槽覆盖不足",
				fmt.Sprintf("集群槽覆盖不完整: %d/16384", covered),
				hostIP,
				"alert",
			)
			messages = append(messages, msg)
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
					bigKeys = append(bigKeys, fmt.Sprintf("%s (size: %d bytes)", key, size))
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
		msg := bot.FormatAlert(
			fmt.Sprintf("Redis (%s)", cfg.ClusterName),
			"大键扫描失败",
			fmt.Sprintf("无法扫描大键: %v", err),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	}
	if len(bigKeys) > 0 {
		msg := bot.FormatAlert(
			fmt.Sprintf("Redis (%s)", cfg.ClusterName),
			"大键检测",
			fmt.Sprintf("发现大键:\n%s", strings.Join(bigKeys, "\n")),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	}

	if len(messages) > 0 {
		return messages, hostIP, fmt.Errorf("Redis issues detected")
	}
	return nil, hostIP, nil
}