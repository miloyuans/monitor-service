package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/monitor"
	"monitor-service/util"
)

func main() {
	// Initialize logger with JSON handler
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Load configuration
	cfg, err := config.LoadConfig("config.yaml")
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

	// Send startup alert
	startupMsg := bot.FormatAlert("Monitor Service ("+cfg.ClusterName+")", "服务启动", "监控服务已启动", "", "startup")
	if err := bot.SendAlert("Monitor Service ("+cfg.ClusterName+")", "服务启动", "监控服务已启动", "", "startup"); err != nil {
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
		shutdownMsg := bot.FormatAlert("Monitor Service ("+cfg.ClusterName+")", "服务停止", "监控服务已停止", "", "shutdown")
		if err := bot.SendAlert("Monitor Service ("+cfg.ClusterName+")", "服务停止", "监控服务已停止", "", "shutdown"); err != nil {
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
	type alertKey struct {
		hash      string
		timestamp time.Time
	}
	alertCache := make(map[string]alertKey)
	alertSilenceDuration := time.Duration(cfg.AlertSilenceDuration) * time.Minute

	for {
		select {
		case <-ctx.Done():
			slog.Info("Monitoring stopped gracefully", "component", "main")
			return
		case <-ticker.C:
			monitorAndAlert(ctx, cfg, bot, alertCache, alertSilenceDuration)
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

// monitorAndAlert runs monitoring tasks and sends deduplicated alerts.
func monitorAndAlert(ctx context.Context, cfg config.Config, bot *alert.AlertBot, alertCache map[string]alertKey, alertSilenceDuration time.Duration) {
	var allMessages []string
	var hostIP string

	// Define monitoring tasks
	monitors := []struct {
		name string
		fn   func(context.Context, config.Config, *alert.AlertBot) ([]string, string, error)
		cfg  any
	}{
		{"General", func(ctx context.Context, cfg config.Config, bot *alert.AlertBot) ([]string, string, error) {
			return monitor.General(ctx, cfg.Monitoring, bot)
		}, cfg.Monitoring},
		{"RabbitMQ", func(ctx context.Context, cfg config.Config, bot *alert.AlertBot) ([]string, string, error) {
			return monitor.RabbitMQ(ctx, cfg.RabbitMQ, bot)
		}, cfg.RabbitMQ},
		{"Redis", func(ctx context.Context, cfg config.Config, bot *alert.AlertBot) ([]string, string, error) {
			return monitor.Redis(ctx, cfg.Redis, bot)
		}, cfg.Redis},
		{"MySQL", func(ctx context.Context, cfg config.Config, bot *alert.AlertBot) ([]string, string, error) {
			return monitor.MySQL(ctx, cfg.MySQL, bot)
		}, cfg.MySQL},
		{"Nacos", func(ctx context.Context, cfg config.Config, bot *alert.AlertBot) ([]string, string, error) {
			return monitor.Nacos(ctx, cfg.Nacos, bot)
		}, cfg.Nacos},
		{"Host", func(ctx context.Context, cfg config.Config, bot *alert.AlertBot) ([]string, string, error) {
			return monitor.Host(ctx, cfg.HostMonitoring, bot)
		}, cfg.HostMonitoring},
		{"System", func(ctx context.Context, cfg config.Config, bot *alert.AlertBot) ([]string, string, error) {
			return monitor.System(ctx, cfg.SystemMonitoring, bot)
		}, cfg.SystemMonitoring},
	}

	// Run enabled monitoring tasks
	for _, m := range monitors {
		// Check if the monitoring task is enabled
		var enabled bool
		switch cfg := m.cfg.(type) {
		case config.GeneralConfig:
			enabled = cfg.Enabled
		case config.RabbitMQConfig:
			enabled = cfg.Enabled
		case config.RedisConfig:
			enabled = cfg.Enabled
		case config.MySQLConfig:
			enabled = cfg.Enabled
		case config.NacosConfig:
			enabled = cfg.Enabled
		case config.HostConfig:
			enabled = cfg.Enabled
		case config.SystemConfig:
			enabled = cfg.Enabled
		}
		if !enabled {
			slog.Debug("Skipping disabled monitoring task", "monitor", m.name, "component", "main")
			continue
		}

		// Execute monitoring task
		messages, ip, err := m.fn(ctx, cfg, bot)
		if err != nil {
			slog.Error("Monitoring task failed", "monitor", m.name, "error", err, "component", "main")
		}
		if len(messages) > 0 {
			allMessages = append(allMessages, messages...)
			if ip != "" {
				hostIP = ip // Use the last non-empty IP
			}
		}
	}

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
			fmt.Fprintf(&details, "**%s - %s**:\n%s", serviceName, eventName, parts[1])
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
		now := time.Now()
		if cache, ok := alertCache[hash]; ok && now.Sub(cache.timestamp) < alertSilenceDuration {
			slog.Info("Skipping duplicate alert", "hash", hash, "component", "main")
			return
		}

		// Format and send combined alert
		serviceName := fmt.Sprintf("Monitor Service (%s)", bot.ClusterName)
		combinedMsg := bot.FormatAlert(serviceName, "服务异常", details.String(), hostIP, "alert")
		if err := bot.SendAlert(serviceName, "服务异常", details.String(), hostIP, "alert"); err != nil {
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
	} else {
		slog.Debug("No alerts generated", "component", "main")
	}
}