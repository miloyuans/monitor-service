package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/pkg/errors"
	"monitor-service/alert"
	"monitor-service/config"
)

// Nacos checks the Nacos service health and configuration availability.
func Nacos(ctx context.Context, cfg config.NacosConfig, bot *alert.AlertBot) ([]string, string, error) {
	var messages []string
	var errs []error
	hostIP := cfg.HostIP // Assume HostIP is added to NacosConfig

	// Configure HTTP client
	client := &http.Client{
		Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second, // Configurable timeout
	}

	// Retry configuration
	const maxRetries = 3
	const retryDelay = 1 * time.Second

	// Check Nacos health with retries
	healthURL := cfg.Address + "/nacos/v1/ns/health"
	var healthResp *http.Response
	for attempt := 1; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			slog.Error("Failed to create Nacos health request", "url", healthURL, "error", err, "component", "nacos")
			errs = append(errs, fmt.Errorf("failed to create Nacos health request: %w", err))
			break
		}

		healthResp, err = client.Do(req)
		if err == nil && healthResp.StatusCode == http.StatusOK {
			break
		}
		if err != nil {
			slog.Warn("Retry attempt", "attempt", attempt, "url", healthURL, "error", err, "component", "nacos")
		} else {
			slog.Warn("Retry attempt", "attempt", attempt, "url", healthURL, "status", healthResp.StatusCode, "component", "nacos")
			healthResp.Body.Close()
		}
		if attempt < maxRetries {
			time.Sleep(retryDelay)
		}
	}

	if healthResp == nil {
		msg := bot.FormatAlert(
			fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
			"健康检查失败",
			"所有重试尝试均失败",
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	} else {
		defer healthResp.Body.Close()
		if healthResp.StatusCode != http.StatusOK {
			msg := bot.FormatAlert(
				fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
				"健康状态异常",
				fmt.Sprintf("健康检查返回非 200 状态码: %d", healthResp.StatusCode),
				hostIP,
				"alert",
			)
			messages = append(messages, msg)
			errs = append(errs, fmt.Errorf("Nacos unhealthy status: %d", healthResp.StatusCode))
		} else {
			body, err := io.ReadAll(healthResp.Body)
			if err != nil {
				slog.Error("Failed to read Nacos health response", "url", healthURL, "error", err, "component", "nacos")
				msg := bot.FormatAlert(
					fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
					"健康响应读取失败",
					fmt.Sprintf("无法读取健康检查响应: %v", err),
					hostIP,
					"alert",
				)
				messages = append(messages, msg)
				errs = append(errs, fmt.Errorf("failed to read Nacos health response: %w", err))
			} else {
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
					messages = append(messages, msg)
					errs = append(errs, fmt.Errorf("failed to unmarshal Nacos health response: %w", err))
				} else if status, ok := health["status"]; !ok || status != "UP" {
					slog.Error("Nacos unhealthy status", "url", healthURL, "status", status, "component", "nacos")
					msg := bot.FormatAlert(
						fmt.Sprintf("Nacos (%s)", cfg.ClusterName),
						"服务状态异常",
						fmt.Sprintf("Nacos 服务状态非 UP: %s", status),
						hostIP,
						"alert",
					)
					messages = append(messages, msg)
					errs = append(errs, fmt.Errorf("Nacos unhealthy status: %s", status))
				}
			}
		}
	}

	// Check configuration service availability
	configURL := fmt.Sprintf("%s/nacos/v1/cs/configs?dataId=%s&group=%s", cfg.Address, cfg.TestDataID, cfg.TestGroup)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, configURL, nil)
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
		errs = append(errs, fmt.Errorf("failed to create Nacos config request: %w", err))
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
			errs = append(errs, fmt.Errorf("failed to get Nacos config: %w", err))
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
				errs = append(errs, fmt.Errorf("Nacos config unhealthy status: %d", resp.StatusCode))
			}
		}
	}

	if len(errs) > 0 {
		return messages, hostIP, errors.Wrap(errors.Join(errs...), "Nacos issues detected")
	}
	return nil, hostIP, nil
}