package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"monitor-service/config"
)

// Nacos checks the Nacos service health.
func Nacos(ctx context.Context, cfg config.NacosConfig) (string, error) {
	url := cfg.Address + "/nacos/v1/ns/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Sprintf("**Nacos (%s)**: Request creation failed: %v", cfg.ClusterName, err), err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Sprintf("**Nacos (%s)**: Get failed: %v", cfg.ClusterName, err), err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("**Nacos (%s)**: Status %d", cfg.ClusterName, resp.StatusCode), fmt.Errorf("unhealthy")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf("**Nacos (%s)**: Read failed: %v", cfg.ClusterName, err), err
	}

	var health map[string]string
	if err := json.Unmarshal(body, &health); err == nil {
		if status, ok := health["status"]; ok && status != "UP" {
			return fmt.Sprintf("**Nacos (%s)**: Status %s", cfg.ClusterName, status), fmt.Errorf("unhealthy")
		}
	}

	return "", nil
}