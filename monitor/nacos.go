package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"monitor-service/config"
)

// Nacos checks the Nacos service health.
func Nacos(ctx context.Context, cfg config.NacosConfig, clusterName string) (string, error) {
	url := cfg.Address + "/nacos/v1/ns/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Error("Failed to create Nacos request", "url", url, "error", err)
		return fmt.Sprintf("**Nacos (%s)**: Request creation failed: %v", clusterName, err), err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("Failed to get Nacos health", "url", url, "error", err)
		return fmt.Sprintf("**Nacos (%s)**: Get failed: %v", clusterName, err), err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("Nacos returned non-OK status", "url", url, "status", resp.StatusCode)
		return fmt.Sprintf("**Nacos (%s)**: Status %d", clusterName, resp.StatusCode), fmt.Errorf("unhealthy")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("Failed to read Nacos response", "url", url, "error", err)
		return fmt.Sprintf("**Nacos (%s)**: Read failed: %v", clusterName, err), err
	}

	var health map[string]string
	if err := json.Unmarshal(body, &health); err != nil {
		slog.Error("Failed to unmarshal Nacos health response", "error", err)
		return fmt.Sprintf("**Nacos (%s)**: Unmarshal failed: %v", clusterName, err), err
	}
	if status, ok := health["status"]; ok && status != "UP" {
		slog.Error("Nacos unhealthy status", "url", url, "status", status)
		return fmt.Sprintf("**Nacos (%s)**: Status %s", clusterName, status), fmt.Errorf("unhealthy")
	}

	return "", nil
}