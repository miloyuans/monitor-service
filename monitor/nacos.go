package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/util"
)

// Nacos monitors Nacos service health and configuration availability.
func Nacos(ctx context.Context, cfg config.NacosConfig, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration) error {
	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "nacos")
		hostIP = "unknown"
	}

	// Validate configuration parameters
	if cfg.Address == "" || cfg.NacosDataID == "" || cfg.NacosGroup == "" {
		details := fmt.Sprintf("Invalid Nacos configuration: Address=%s, NacosDataID=%s, NacosGroup=%s", cfg.Address, cfg.NacosDataID, cfg.NacosGroup)
		slog.Error("Invalid Nacos configuration", "address", cfg.Address, "nacos_data_id", cfg.NacosDataID, "nacos_group", cfg.NacosGroup, "component", "nacos")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "配置参数异常", details, hostIP, "alert", "nacos", map[string]interface{}{})
	}

	// Validate and normalize Address
	address := cfg.Address
	if !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "https://") {
		address = "http://" + address
	}
	if _, err := url.Parse(address); err != nil {
		details := fmt.Sprintf("Invalid Nacos address URL: %v", err)
		slog.Error("Invalid Nacos address", "address", cfg.Address, "error", err, "component", "nacos")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "地址格式异常", details, hostIP, "alert", "nacos", map[string]interface{}{})
	}

	// Configure HTTP client with optimized settings
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 5 * time.Second,
			DisableKeepAlives:   false,
		},
	}

	// Check Nacos health
	healthURL := fmt.Sprintf("%s/nacos/v1/ns/health", address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		slog.Error("Failed to create Nacos health request", "url", healthURL, "error", err, "component", "nacos")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "健康检查请求失败", fmt.Sprintf("无法创建健康检查请求: %v", err), hostIP, "alert", "nacos", map[string]interface{}{})
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Failed to get Nacos health", "url", healthURL, "error", err, "component", "nacos")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "健康检查失败", fmt.Sprintf("无法获取健康状态: %v", err), hostIP, "alert", "nacos", map[string]interface{}{})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("Nacos returned non-OK status", "url", healthURL, "status_code", resp.StatusCode, "component", "nacos")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "健康状态异常", fmt.Sprintf("健康检查返回非 200 状态码: %d", resp.StatusCode), hostIP, "alert", "nacos", map[string]interface{}{})
	}

	// Read response with limited buffer
	const maxBodySize = 1 << 20 // 1MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		slog.Error("Failed to read Nacos health response", "url", healthURL, "error", err, "component", "nacos")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "健康响应读取失败", fmt.Sprintf("无法读取健康检查响应: %v", err), hostIP, "alert", "nacos", map[string]interface{}{})
	}

	var health map[string]string
	if err := json.Unmarshal(body, &health); err != nil {
		slog.Error("Failed to unmarshal Nacos health response", "url", healthURL, "error", err, "component", "nacos")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "健康响应解析失败", fmt.Sprintf("无法解析健康检查响应: %v", err), hostIP, "alert", "nacos", map[string]interface{}{})
	}

	if status, ok := health["status"]; !ok || status != "UP" {
		details := fmt.Sprintf("Nacos 服务状态非 UP: %s", alert.EscapeMarkdown(status))
		slog.Error("Nacos unhealthy status", "url", healthURL, "status", status, "component", "nacos")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "服务状态异常", details, hostIP, "alert", "nacos", map[string]interface{}{"status": status})
	}

	// Check configuration service availability
	configURL := fmt.Sprintf("%s/nacos/v1/cs/configs?dataId=%s&group=%s", address, alert.EscapeMarkdown(cfg.NacosDataID), alert.EscapeMarkdown(cfg.NacosGroup))
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, configURL, nil)
	if err != nil {
		slog.Error("Failed to create Nacos config request", "url", configURL, "data_id", cfg.NacosDataID, "group", cfg.NacosGroup, "error", err, "component", "nacos")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "配置服务请求失败", fmt.Sprintf("无法创建配置服务请求: %v", err), hostIP, "alert", "nacos", map[string]interface{}{})
	}

	resp, err = client.Do(req)
	if err != nil {
		slog.Error("Failed to get Nacos config", "url", configURL, "data_id", cfg.NacosDataID, "group", cfg.NacosGroup, "error", err, "component", "nacos")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "配置服务访问失败", fmt.Sprintf("无法访问配置服务: %v", err), hostIP, "alert", "nacos", map[string]interface{}{})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("Nacos config returned non-OK status", "url", configURL, "data_id", cfg.NacosDataID, "group", cfg.NacosGroup, "status_code", resp.StatusCode, "component", "nacos")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "配置服务状态异常", fmt.Sprintf("配置服务返回非 200 状态码: %d", resp.StatusCode), hostIP, "alert", "nacos", map[string]interface{}{"status_code": resp.StatusCode})
	}

	slog.Debug("No Nacos issues detected", "health_url", healthURL, "config_url", configURL, "data_id", cfg.NacosDataID, "group", cfg.NacosGroup, "component", "nacos")
	return nil
}