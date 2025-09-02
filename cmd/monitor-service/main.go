package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/monitor"
	"monitor-service/util"
)

// alertKey defines the structure for deduplication cache entries.
type alertKey struct {
	hash      string
	timestamp time.Time
}

func main() {
	// Initialize logger with JSON handler
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Load configuration
	cfg, err := config.LoadConfig("/app/config.yaml")
	if err != nil {
		slog.Error("Failed to load configuration", "error", err, "component", "main")
		os.Exit(1)
	}

	// Initialize alert bot
	bot, err := alert.NewAlertBot(cfg.Telegram.BotToken, cfg.Telegram.ChatID, cfg.ClusterName, cfg.ShowHostname)
	if err != nil {
		slog.Error("Failed to initialize alert bot", "error", err, "component", "main")
		os.Exit(1)
	}

	// Get private IP for startup and shutdown alerts
	ctx := context.Background()
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP for startup alert", "error", err, "component", "main")
		hostIP = "unknown"
	}

	// Send startup alert
	startupMsg := bot.FormatAlert(fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "✅启动✅", "监控服务已启动✅", hostIP, "startup")
	if err := bot.SendAlert(ctx, fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "✅启动✅", "监控服务已启动✅", hostIP, "startup"); err != nil {
		slog.Error("Failed to send startup alert", "error", err, "component", "main")
	} else {
		slog.Info("Sent startup alert", "message", startupMsg, "component", "main")
	}

	// Log enabled monitoring features
	logMonitoringStatus(cfg)

	// Parse check interval
	interval, err := time.ParseDuration(cfg.CheckInterval)
	if err != nil {
		slog.Error("Invalid check interval", "interval", cfg.CheckInterval, "error", err, "component", "main")
		startupMsg = bot.FormatAlert(fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "异常", fmt.Sprintf("无效的检查间隔: %v", err), hostIP, "alert")
		if err := bot.SendAlert(ctx, fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "异常", fmt.Sprintf("无效的检查间隔: %v", err), hostIP, "alert"); err != nil {
			slog.Error("Failed to send alert", "error", err, "component", "main")
		}
		os.Exit(1)
	}

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("Received signal, initiating shutdown", "signal", sig, "component", "main")
		// Send shutdown alert
		shutdownMsg := bot.FormatAlert(fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "❌停止❌", "监控服务已停止❌", hostIP, "shutdown")
		if err := bot.SendAlert(ctx, fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "❌停止❌", "监控服务已停止❌", hostIP, "shutdown"); err != nil {
			slog.Error("Failed to send shutdown alert", "error", err, "component", "main")
		} else {
			slog.Info("Sent shutdown alert", "message", shutdownMsg, "component", "main")
		}
		cancel()
	}()

	// Start monitoring
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Alert deduplication cache
	alertCache := make(map[string]alertKey)
	var cacheMutex sync.Mutex // Protect concurrent access to alertCache
	alertSilenceDuration := time.Duration(cfg.AlertSilenceDuration) * time.Minute

	for {
		select {
		case <-ctx.Done():
			slog.Info("Monitoring stopped gracefully", "component", "main")
			return
		case <-ticker.C:
			monitorAndAlert(ctx, cfg, bot, alertCache, &cacheMutex, alertSilenceDuration, interval)
		}
	}
}

// logMonitoringStatus logs the status of all monitoring features.
func logMonitoringStatus(cfg config.Config) {
	features := []struct {
		name    string
		enabled bool
	}{
		{"General Monitoring", cfg.Monitoring.Enabled},
		{"RabbitMQ Monitoring", cfg.RabbitMQ.Enabled},
		{"Redis Monitoring", cfg.Redis.Enabled},
		{"MySQL Monitoring", cfg.MySQL.Enabled},
		{"Nacos Monitoring", cfg.Nacos.Enabled},
		{"Host Monitoring", cfg.HostMonitoring.Enabled},
		{"System Monitoring", cfg.SystemMonitoring.Enabled},
	}

	for _, feature := range features {
		if feature.enabled {
			slog.Info("Monitoring feature enabled", "feature", feature.name, "component", "main")
		} else {
			slog.Info("Monitoring feature disabled", "feature", feature.name, "message", fmt.Sprintf("%s is not active", feature.name), "component", "main")
		}
	}
}

// monitorAndAlert runs monitoring tasks concurrently and sends deduplicated alerts.
func monitorAndAlert(ctx context.Context, cfg config.Config, bot *alert.AlertBot, alertCache map[string]alertKey, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, checkInterval time.Duration) {
	var allMessages []string
	var hostIP string
	var mu sync.Mutex // Protect allMessages and hostIP
	var wg sync.WaitGroup

	// Define monitoring tasks
	monitors := []struct {
		name        string
		enabled     bool
		fn          func(context.Context, interface{}, *alert.AlertBot) ([]string, string, error)
		cfg         interface{}
		serviceName string
	}{
		{
			name:        "General",
			enabled:     cfg.Monitoring.Enabled,
			fn:          func(ctx context.Context, cfg interface{}, bot *alert.AlertBot) ([]string, string, error) { return monitor.General(ctx, cfg.(config.GeneralConfig), bot) },
			cfg:         cfg.Monitoring,
			serviceName: "通用告警",
		},
		{
			name:        "RabbitMQ",
			enabled:     cfg.RabbitMQ.Enabled,
			fn:          func(ctx context.Context, cfg interface{}, bot *alert.AlertBot) ([]string, string, error) { return monitor.RabbitMQ(ctx, cfg.(config.RabbitMQConfig), bot) },
			cfg:         cfg.RabbitMQ,
			serviceName: "RabbitMQ告警",
		},
		{
			name:        "Redis",
			enabled:     cfg.Redis.Enabled,
			fn:          func(ctx context.Context, cfg interface{}, bot *alert.AlertBot) ([]string, string, error) { return monitor.Redis(ctx, cfg.(config.RedisConfig), bot) },
			cfg:         cfg.Redis,
			serviceName: "Redis告警",
		},
		{
			name:        "MySQL",
			enabled:     cfg.MySQL.Enabled,
			fn:          func(ctx context.Context, cfg interface{}, bot *alert.AlertBot) ([]string, string, error) { return monitor.MySQL(ctx, cfg.(config.MySQLConfig), bot) },
			cfg:         cfg.MySQL,
			serviceName: "数据库告警",
		},
		{
			name:        "Nacos",
			enabled:     cfg.Nacos.Enabled,
			fn:          func(ctx context.Context, cfg interface{}, bot *alert.AlertBot) ([]string, string, error) { return monitor.Nacos(ctx, cfg.(config.NacosConfig), bot) },
			cfg:         cfg.Nacos,
			serviceName: "Nacos告警",
		},
		{
			name:        "Host",
			enabled:     cfg.HostMonitoring.Enabled,
			fn:          func(ctx context.Context, cfg interface{}, bot *alert.AlertBot) ([]string, string, error) { return monitor.Host(ctx, cfg.(config.HostConfig), bot) },
			cfg:         cfg.HostMonitoring,
			serviceName: "主机告警",
		},
		{
			name:        "System",
			enabled:     cfg.SystemMonitoring.Enabled,
			fn:          func(ctx context.Context, cfg interface{}, bot *alert.AlertBot) ([]string, string, error) { return monitor.System(ctx, cfg.(config.SystemConfig), bot) },
			cfg:         cfg.SystemMonitoring,
			serviceName: "系统告警",
		},
	}

	// Run enabled monitoring tasks concurrently
	for _, m := range monitors {
		if !m.enabled {
			slog.Debug("Skipping disabled monitoring task", "monitor", m.name, "component", "main")
			continue
		}

		wg.Add(1)
		go func(name, serviceName string, fn func(context.Context, interface{}, *alert.AlertBot) ([]string, string, error), cfg interface{}) {
			defer wg.Done()
			// Create a task-specific context with timeout
			taskCtx, taskCancel := context.WithTimeout(ctx, checkInterval)
			defer taskCancel()

			messages, ip, err := fn(taskCtx, cfg, bot)
			if err != nil {
				slog.Error("Monitoring task failed", "monitor", name, "error", err, "component", "main")
			}
			if len(messages) > 0 {
				mu.Lock()
				// Update serviceName in alert messages to reflect module-specific name
				for i, msg := range messages {
					parts := strings.SplitN(msg, "*服务名*: ", 2)
					if len(parts) == 2 {
						lines := strings.SplitN(parts[1], "\n", 2)
						if len(lines) == 2 {
							messages[i] = parts[0] + "*服务名*: " + alert.EscapeMarkdown(serviceName) + "\n" + lines[1]
						}
					}
				}
				allMessages = append(allMessages, messages...)
				if ip != "" {
					hostIP = ip // Use the last non-empty IP
				}
				mu.Unlock()
			}
		}(m.name, m.serviceName, m.fn, m.cfg)
	}
	wg.Wait()

	// Combine and deduplicate alerts
	if len(allMessages) > 0 {
		var details strings.Builder
		for i, msg := range allMessages {
			// Extract details by splitting on "*详情*:\n"
			parts := strings.SplitN(msg, "*详情*:\n", 2)
			if len(parts) != 2 {
				slog.Warn("Invalid alert format, skipping", "message", msg, "component", "main")
				continue
			}
			// Extract serviceName and eventName
			serviceName := ""
			eventName := ""
			for _, line := range strings.Split(parts[0], "\n") {
				if strings.HasPrefix(line, "*服务名*: ") {
					serviceName = strings.TrimPrefix(line, "*服务名*: ")
				}
				if strings.HasPrefix(line, "*事件名*: ") {
					eventName = strings.TrimPrefix(line, "*事件名*: ")
				}
			}
			if serviceName == "" || eventName == "" {
				slog.Warn("Missing serviceName or eventName, skipping", "message", msg, "component", "main")
				continue
			}
			if i > 0 {
				details.WriteString("\n\n")
			}
			fmt.Fprintf(&details, "**%s - %s**:\n%s", alert.EscapeMarkdown(serviceName), alert.EscapeMarkdown(eventName), alert.EscapeMarkdown(parts[1]))
		}

		if details.Len() == 0 {
			slog.Info("No valid alert details to send", "component", "main")
			return
		}

		// Deduplicate combined alert
		hash, err := util.MD5Hash(details.String())
		if err != nil {
			slog.Error("Failed to generate alert hash", "error", err, "component", "main")
			return
		}
		cacheMutex.Lock()
		now := time.Now()
		if cache, ok := alertCache[hash]; ok && now.Sub(cache.timestamp) < alertSilenceDuration {
			slog.Info("Skipping duplicate alert", "hash", hash, "component", "main")
			cacheMutex.Unlock()
			return
		}

		// Format and send combined alert
		serviceName := fmt.Sprintf("Monitor Service %s", cfg.ClusterName)
		combinedMsg := bot.FormatAlert(serviceName, "服务异常", details.String(), hostIP, "alert")
		slog.Debug("Sending combined alert", "message", combinedMsg, "component", "main")
		if err := bot.SendAlert(ctx, serviceName, "服务异常", details.String(), hostIP, "alert"); err != nil {
			slog.Error("Failed to send combined alert", "error", err, "component", "main")
		} else {
			slog.Info("Sent combined alert", "message", combinedMsg, "component", "main")
			alertCache[hash] = alertKey{hash: hash, timestamp: now}
		}

		// Clean up old cache entries
		for h, cache := range alertCache {
			if now.Sub(cache.timestamp) >= alertSilenceDuration {
				delete(alertCache, h)
				slog.Debug("Removed expired alert cache entry", "hash", h, "component", "main")
			}
		}
		cacheMutex.Unlock()
	} else {
		slog.Debug("No alerts generated", "component", "main")
	}
}