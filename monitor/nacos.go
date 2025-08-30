package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"monitor-service/alert"
	"monitor-service/config"
)

// Nacos checks the Nacos service health and configuration availability.
func Nacos(ctx context.Context, cfg config.NacosConfig, bot *alert.AlertBot) ([]string, string, error) {
	var hostIP string // Placeholder for host IP, to be implemented if needed
	var messages []string

	// Configure HTTP client with timeout
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Check Nacos health
	healthURL := cfg.Address + "/nacos/v1/ns/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		slog.Error("Failed to create Nacos health request", "url", healthURL, "error", err, "component", "nacos")
		msg := bot.FormatAlert(
			fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
			"健康检查请求失败",
			fmt.Sprintf("无法创建健康检查请求: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to create Nacos health request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Failed to get Nacos health", "url", healthURL, "error", err, "component", "nacos")
		msg := bot.FormatAlert(
			fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
			"健康检查失败",
			fmt.Sprintf("无法获取健康状态: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to get Nacos health: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("Nacos returned non-OK status", "url", healthURL, "status", resp.StatusCode, "component", "nacos")
		msg := bot.FormatAlert(
			fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
			"健康状态异常",
			fmt.Sprintf("健康检查返回非 200 状态码: %d", resp.StatusCode),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("Nacos unhealthy status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("Failed to read Nacos health response", "url", healthURL, "error", err, "component", "nacos")
		msg := bot.FormatAlert(
			fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
			"健康响应读取失败",
			fmt.Sprintf("无法读取健康检查响应: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to read Nacos health response: %w", err)
	}

	var health map[string]string
	if err := json.Unmarshal(body, &health); err != nil {
		slog.Error("Failed to unmarshal Nacos health response", "error", err, "component", "nacos")
		msg := bot.FormatAlert(
			fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
			"健康响应解析失败",
			fmt.Sprintf("无法解析健康检查响应: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to unmarshal Nacos health response: %w", err)
	}

	if status, ok := health["status"]; !ok || status != "UP" {
		slog.Error("Nacos unhealthy status", "url", healthURL, "status", status, "component", "nacos")
		msg := bot.FormatAlert(
			fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
			"服务状态异常",
			fmt.Sprintf("Nacos 服务状态非 UP: %s", status),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	}

	// Check configuration service availability (example endpoint)
	configURL := cfg.Address + "/nacos/v1/cs/configs?dataId=test&group=DEFAULT_GROUP"
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, configURL, nil)
	if err != nil {
		slog.Error("Failed to create Nacos config request", "url", configURL, "error", err, "component", "nacos")
		msg := bot.FormatAlert(
			fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
			"配置服务请求失败",
			fmt.Sprintf("无法创建配置服务请求: %v", err),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	} else {
		resp, err := client.Do(req)
		if err != nil {
			slog.Error("Failed to get Nacos config", "url", configURL, "error", err, "component", "nacos")
			msg := bot.FormatAlert(
				fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
				"配置服务访问失败",
				fmt.Sprintf("无法访问配置服务: %v", err),
				hostIP,
				"alert",
			)
			messages = append(messages, msg)
		} else {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				slog.Error("Nacos config returned non-OK status", "url", configURL, "status", resp.StatusCode, "component", "nacos")
				msg := bot.FormatAlert(
					fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
					"配置服务状态异常",
					fmt.Sprintf("配置服务返回非 200 状态码: %d", resp.StatusCode),
					hostIP,
					"alert",
				)
				messages = append(messages, msg)
			}
		}
	}

	if len(messages) > 0 {
		return messages, hostIP, fmt.Errorf("Nacos issues detected")
	}
	return nil, hostIP, nil
}