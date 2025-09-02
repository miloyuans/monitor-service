package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	if cfg.NacosDataID == "" || cfg.NacosGroup == "" {
		details := fmt.Sprintf("配置参数无效: NacosDataID=%s, NacosGroup=%s", cfg.NacosDataID, cfg.NacosGroup)
		slog.Error("Invalid Nacos configuration", "nacos_data_id", cfg.NacosDataID, "nacos_group", cfg.NacosGroup, "component", "nacos")
		msg := bot.FormatAlert("Nacos告警", "配置参数异常", details, hostIP, "alert")
		return sendNacosAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "配置参数异常", details, hostIP, "alert", msg)
	}

	// Configure HTTP client with timeout
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Check Nacos health
	healthURL := fmt.Sprintf("%s/nacos/v1/ns/health", cfg.Address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		slog.Error("Failed to create Nacos health request", "url", healthURL, "error", err, "component", "nacos")
		details := fmt.Sprintf("无法创建健康检查请求: %v", err)
		msg := bot.FormatAlert("Nacos告警", "健康检查请求失败", details, hostIP, "alert")
		return sendNacosAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "健康检查请求失败", details, hostIP, "alert", msg)
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Failed to get Nacos health", "url", healthURL, "error", err, "component", "nacos")
		details := fmt.Sprintf("无法获取健康状态: %v", err)
		msg := bot.FormatAlert("Nacos告警", "健康检查失败", details, hostIP, "alert")
		return sendNacosAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "健康检查失败", details, hostIP, "alert", msg)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("Nacos returned non-OK status", "url", healthURL, "status", resp.StatusCode, "component", "nacos")
		details := fmt.Sprintf("健康检查返回非 200 状态码: %d", resp.StatusCode)
		msg := bot.FormatAlert("Nacos告警", "健康状态异常", details, hostIP, "alert")
		return sendNacosAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "健康状态异常", details, hostIP, "alert", msg)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("Failed to read Nacos health response", "url", healthURL, "error", err, "component", "nacos")
		details := fmt.Sprintf("无法读取健康检查响应: %v", err)
		msg := bot.FormatAlert("Nacos告警", "健康响应读取失败", details, hostIP, "alert")
		return sendNacosAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "健康响应读取失败", details, hostIP, "alert", msg)
	}

	var health map[string]string
	if err := json.Unmarshal(body, &health); err != nil {
		slog.Error("Failed to unmarshal Nacos health response", "error", err, "component", "nacos")
		details := fmt.Sprintf("无法解析健康检查响应: %v", err)
		msg := bot.FormatAlert("Nacos告警", "健康响应解析失败", details, hostIP, "alert")
		return sendNacosAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "健康响应解析失败", details, hostIP, "alert", msg)
	}

	if status, ok := health["status"]; !ok || status != "UP" {
		slog.Error("Nacos unhealthy status", "url", healthURL, "status", status, "component", "nacos")
		details := fmt.Sprintf("Nacos 服务状态非 UP: %s", alert.EscapeMarkdown(status))
		msg := bot.FormatAlert("Nacos告警", "服务状态异常", details, hostIP, "alert")
		if err := sendNacosAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "服务状态异常", details, hostIP, "alert", msg); err != nil {
			return err
		}
	}

	// Check configuration service availability
	configURL := fmt.Sprintf("%s/nacos/v1/cs/configs?dataId=%s&group=%s", cfg.Address, alert.EscapeMarkdown(cfg.NacosDataID), alert.EscapeMarkdown(cfg.NacosGroup))
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, configURL, nil)
	if err != nil {
		slog.Error("Failed to create Nacos config request", "url", configURL, "data_id", cfg.NacosDataID, "group", cfg.NacosGroup, "error", err, "component", "nacos")
		details := fmt.Sprintf("无法创建配置服务请求: %v", err)
		msg := bot.FormatAlert("Nacos告警", "配置服务请求失败", details, hostIP, "alert")
		return sendNacosAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "配置服务请求失败", details, hostIP, "alert", msg)
	}

	resp, err = client.Do(req)
	if err != nil {
		slog.Error("Failed to get Nacos config", "url", configURL, "data_id", cfg.NacosDataID, "group", cfg.NacosGroup, "error", err, "component", "nacos")
		details := fmt.Sprintf("无法访问配置服务: %v", err)
		msg := bot.FormatAlert("Nacos告警", "配置服务访问失败", details, hostIP, "alert")
		return sendNacosAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "配置服务访问失败", details, hostIP, "alert", msg)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("Nacos config returned non-OK status", "url", configURL, "data_id", cfg.NacosDataID, "group", cfg.NacosGroup, "status", resp.StatusCode, "component", "nacos")
		details := fmt.Sprintf("配置服务返回非 200 状态码: %d", resp.StatusCode)
		msg := bot.FormatAlert("Nacos告警", "配置服务状态异常", details, hostIP, "alert")
		return sendNacosAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "Nacos告警", "配置服务状态异常", details, hostIP, "alert", msg)
	}

	slog.Debug("No Nacos issues detected", "component", "nacos")
	return nil
}

// sendNacosAlert sends a deduplicated Telegram alert for the Nacos module.
func sendNacosAlert(ctx context.Context, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, serviceName, eventName, details, hostIP, alertType, message string) error {
	hash, err := util.MD5Hash(details)
	if err != nil {
		slog.Error("Failed to generate alert hash", "error", err, "component", "nacos")
		return fmt.Errorf("failed to generate alert hash: %w", err)
	}
	cacheMutex.Lock()
	now := time.Now()
	if timestamp, ok := alertCache[hash]; ok && now.Sub(timestamp) < alertSilenceDuration {
		slog.Info("Skipping duplicate alert", "hash", hash, "component", "nacos")
		cacheMutex.Unlock()
		return nil
	}
	alertCache[hash] = now
	// Clean up old cache entries
	for h, t := range alertCache {
		if now.Sub(t) >= alertSilenceDuration {
			delete(alertCache, h)
			slog.Debug("Removed expired alert cache entry", "hash", h, "component", "nacos")
		}
	}
	cacheMutex.Unlock()
	slog.Debug("Sending alert", "message", message, "component", "nacos")
	if err := bot.SendAlert(ctx, serviceName, eventName, details, hostIP, alertType); err != nil {
		slog.Error("Failed to send alert", "error", err, "component", "nacos")
		return fmt.Errorf("failed to send alert: %w", err)
	}
	slog.Info("Sent alert", "message", message, "component", "nacos")
	return nil
}