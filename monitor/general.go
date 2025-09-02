package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/util"
)

// General monitors general system metrics and sends alerts.
func General(ctx context.Context, cfg config.GeneralConfig, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration) error {
	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "general")
		hostIP = "unknown"
	}

	// Placeholder: Check system uptime as an example
	uptime, err := getSystemUptime()
	if err != nil {
		slog.Error("Failed to get system uptime", "error", err, "component", "general")
		msg := bot.FormatAlert("通用告警", "服务异常", fmt.Sprintf("无法获取系统运行时间: %v", err), hostIP, "alert")
		return sendGeneralAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "通用告警", "服务异常", fmt.Sprintf("无法获取系统运行时间: %v", err), hostIP, "alert", msg)
	}

	// Example threshold: Alert if uptime is less than 5 minutes (recent reboot)
	const minUptime = 5 * time.Minute
	if uptime < minUptime {
		details := fmt.Sprintf("系统运行时间过短: %v < %v", uptime, minUptime)
		slog.Info("System uptime issue detected", "uptime", uptime, "threshold", minUptime, "component", "general")
		msg := bot.FormatAlert("通用告警", "服务异常", details, hostIP, "alert")
		return sendGeneralAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "通用告警", "服务异常", details, hostIP, "alert", msg)
	}

	slog.Debug("No general issues detected", "component", "general")
	return nil
}

// sendGeneralAlert sends a deduplicated Telegram alert for the General module.
func sendGeneralAlert(ctx context.Context, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, serviceName, eventName, details, hostIP, alertType, message string) error {
	hash, err := util.MD5Hash(details)
	if err != nil {
		slog.Error("Failed to generate alert hash", "error", err, "component", "general")
		return fmt.Errorf("failed to generate alert hash: %w", err)
	}
	cacheMutex.Lock()
	now := time.Now()
	if timestamp, ok := alertCache[hash]; ok && now.Sub(timestamp) < alertSilenceDuration {
		slog.Info("Skipping duplicate alert", "hash", hash, "component", "general")
		cacheMutex.Unlock()
		return nil
	}
	alertCache[hash] = now
	// Clean up old cache entries
	for h, t := range alertCache {
		if now.Sub(t) >= alertSilenceDuration {
			delete(alertCache, h)
			slog.Debug("Removed expired alert cache entry", "hash", h, "component", "general")
		}
	}
	cacheMutex.Unlock()
	slog.Debug("Sending alert", "message", message, "component", "general")
	if err := bot.SendAlert(ctx, serviceName, eventName, details, hostIP, alertType); err != nil {
		slog.Error("Failed to send alert", "error", err, "component", "general")
		return fmt.Errorf("failed to send alert: %w", err)
	}
	slog.Info("Sent alert", "message", message, "component", "general")
	return nil
}

// getSystemUptime returns the system uptime (placeholder implementation).
func getSystemUptime() (time.Duration, error) {
	// Placeholder: Implement actual uptime check using system calls or libraries
	// For example, on Linux, read /proc/uptime
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("failed to read /proc/uptime: %w", err)
	}
	parts := strings.Fields(string(data))
	if len(parts) < 1 {
		return 0, fmt.Errorf("invalid /proc/uptime format")
	}
	seconds, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse uptime: %w", err)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}