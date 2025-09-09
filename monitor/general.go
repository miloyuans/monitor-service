package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
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

	// Check system uptime with context timeout
	uptimeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	uptime, err := getSystemUptime(uptimeCtx)
	if err != nil {
		slog.Error("Failed to get system uptime", "error", err, "component", "general")
		details := fmt.Sprintf("Failed to get system uptime: %v", err)
		return sendGeneralAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "General Monitor", "Service Error", details, hostIP, "alert", "general", nil)
	}

	// Check if uptime is below threshold (e.g., recent reboot)
	const minUptime = 5 * time.Minute
	if uptime < minUptime {
		details := fmt.Sprintf("System uptime too short: %v < %v", uptime, minUptime)
		slog.Info("System uptime issue detected", "uptime", uptime.String(), "threshold", minUptime.String(), "component", "general")
		return sendGeneralAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "General Monitor", "Service Error", details, hostIP, "alert", "general", nil)
	}

	slog.Debug("No general issues detected", "component", "general")
	return nil
}

// sendGeneralAlert sends a deduplicated alert for the General module.
func sendGeneralAlert(ctx context.Context, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, serviceName, eventName, details, hostIP, alertType, module string, specificFields map[string]interface{}) error {
	// Generate alert hash for deduplication
	hash, err := util.MD5Hash(details)
	if err != nil {
		slog.Error("Failed to generate alert hash", "error", err, "component", "general")
		return fmt.Errorf("failed to generate alert hash: %w", err)
	}

	// Check and update cache for deduplication
	cacheMutex.Lock()
	now := time.Now()
	if timestamp, ok := alertCache[hash]; ok && now.Sub(timestamp) < alertSilenceDuration {
		slog.Info("Skipping duplicate alert", "hash", hash, "service_name", serviceName, "event_name", eventName, "component", "general")
		cacheMutex.Unlock()
		return nil
	}
	alertCache[hash] = now

	// Clean up expired cache entries
	for h, t := range alertCache {
		if now.Sub(t) >= alertSilenceDuration {
			delete(alertCache, h)
			slog.Debug("Removed expired alert cache entry", "hash", h, "component", "general")
		}
	}
	cacheMutex.Unlock()

	// Generate Telegram message for logging
	message := ""
	if bot != nil {
		message = bot.FormatAlert(serviceName, eventName, details, hostIP, alertType)
	}

	// Send alert to Telegram and monitor-web
	slog.Debug("Sending alert", "message", message, "service_name", serviceName, "event_name", eventName, "module", module, "component", "general")
	if err := bot.SendAlert(ctx, serviceName, eventName, details, hostIP, alertType, module, specificFields); err != nil {
		slog.Error("Failed to send alert", "error", err, "service_name", serviceName, "event_name", eventName, "module", module, "component", "general")
		return fmt.Errorf("failed to send alert: %w", err)
	}
	slog.Info("Sent alert", "message", message, "service_name", serviceName, "event_name", eventName, "module", module, "component", "general")
	return nil
}

// getSystemUptime returns the system uptime.
func getSystemUptime(ctx context.Context) (time.Duration, error) {
	// Read /proc/uptime with context
	dataCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		data, err := os.ReadFile("/proc/uptime")
		if err != nil {
			errCh <- fmt.Errorf("failed to read /proc/uptime: %w", err)
			return
		}
		dataCh <- data
	}()

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case err := <-errCh:
		return 0, err
	case data := <-dataCh:
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
}