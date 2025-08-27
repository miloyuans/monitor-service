package monitor

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
	"monitor-service/config"
)

// Redis checks the Redis cluster for connectivity, node failures, slot coverage, and big keys.
func Redis(ctx context.Context, cfg config.RedisConfig, clusterName string) ([]string, error) {
	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:    []string{cfg.Addr},
		Password: cfg.Password,
	})
	defer client.Close()

	clusterPrefix := fmt.Sprintf("**Redis (%s)**", clusterName)
	msgs := []string{}

	pong, err := client.Ping(ctx).Result()
	if err != nil || pong != "PONG" {
		return []string{fmt.Sprintf("%s: Connection failed: %v", clusterPrefix, err)}, err
	}

	// Check nodes.
	nodes, err := client.ClusterNodes(ctx).Result()
	if err != nil {
		return []string{fmt.Sprintf("%s: Failed to get cluster nodes: %v", clusterPrefix, err)}, err
	}
	lines := strings.Split(nodes, "\n")
	failedNodes := []string{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		if strings.Contains(line, "fail") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				failedNodes = append(failedNodes, fields[1]) // addr
			}
		}
	}
	if len(failedNodes) > 0 {
		msgs = append(msgs, fmt.Sprintf("%s: Failed nodes: %s", clusterPrefix, strings.Join(failedNodes, ", ")))
	}

	// Check slots.
	slots, err := client.ClusterSlots(ctx).Result()
	if err != nil {
		msgs = append(msgs, fmt.Sprintf("%s: Failed to get cluster slots: %v", clusterPrefix, err))
	} else {
		covered := 0
		for _, slot := range slots {
			covered += int(slot.End - slot.Start + 1)
		}
		if covered != 16384 {
			msgs = append(msgs, fmt.Sprintf("%s: Incomplete slot coverage: %d/16384", clusterPrefix, covered))
		}
	}

	// Check big keys.
	bigKeys := []string{}
	err = client.ForEachMaster(ctx, func(ctx context.Context, master *redis.Client) error {
		var cursor uint64
		for {
			var keys []string
			var err error
			keys, cursor, err = master.Scan(ctx, cursor, "*", 100).Result()
			if err != nil {
				return err
			}
			for _, key := range keys {
				size, err := master.MemoryUsage(ctx, key).Result()
				if err == nil && size > cfg.BigKeyThreshold {
					bigKeys = append(bigKeys, fmt.Sprintf("%s (size: %d)", key, size))
				}
			}
			if cursor == 0 {
				break
			}
		}
		return nil
	})
	if err != nil {
		msgs = append(msgs, fmt.Sprintf("%s: Failed to scan for big keys: %v", clusterPrefix, err))
	}
	if len(bigKeys) > 0 {
		msgs = append(msgs, fmt.Sprintf("%s: Big keys found:\n%s", clusterPrefix, strings.Join(bigKeys, "\n")))
	}

	if len(msgs) > 0 {
		return msgs, fmt.Errorf("redis issues")
	}
	return nil, nil
}