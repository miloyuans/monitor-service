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
	// Initialize logger
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Load configuration
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	// Initialize alert bot
	bot, err := alert.NewAlertBot(cfg.Telegram.BotToken, cfg.Telegram.ChatID, cfg.ClusterName, cfg.ShowHostname)
	if err != nil {
		slog.Error("Failed to initialize alert bot", "error", err)
		os.Exit(1)
	}

	// Send startup alert
	startupMsg := bot.FormatAlert("Monitor Service ("+cfg.ClusterName+")", "服务启动", "监控服务已启动", "", "startup")
	if err := bot.SendAlert("Monitor Service ("+cfg.ClusterName+")", "服务启动", "监控服务已启动", "", "startup"); err != nil {
		slog.Error("Failed to send startup alert", "error", err)
	} else {
		slog.Info("Sent startup alert", "message", startupMsg)
	}

	// Log enabled monitoring features
	logMonitoringStatus(cfg)

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("Received signal, shutting down", "signal", sig)
		// Send shutdown alert
		shutdownMsg := bot.FormatAlert("Monitor Service ("+cfg.ClusterName+")", "服务停止", "监控服务已停止", "", "shutdown")
		if err := bot.SendAlert("Monitor Service ("+cfg.ClusterName+")", "服务停止", "监控服务已停止", "", "shutdown"); err != nil {
			slog.Error("Failed to send shutdown alert", "error", err)
		} else {
			slog.Info("Sent shutdown alert", "message", shutdownMsg)
		}
		cancel()
	}()

	// Parse check interval
	interval, err := time.ParseDuration(cfg.CheckInterval)
	if err != nil {
		slog.Error("Invalid check interval", "interval", cfg.CheckInterval, "error", err)
		os.Exit(1)
	}

	// Start monitoring
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Alert deduplication
	type alertKey struct {
		hash      string
		timestamp time.Time
	}
	alertCache := make(map[string]alertKey)
	alertSilenceDuration := time.Duration(cfg.AlertSilenceDuration) * time.Minute

	for {
		select {
		case <-ctx.Done():
			slog.Info("Monitoring stopped")
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
			slog.Info("Monitoring feature enabled", "feature", feature.name)
		} else {
			slog.Info("Monitoring feature disabled", "feature", feature.name, "message", fmt.Sprintf("%s is not active", feature.name))
		}
	}
}

// monitorAndAlert runs monitoring tasks and sends alerts using the alert module.
func monitorAndAlert(ctx context.Context, cfg config.Config, bot *alert.AlertBot, alertCache map[string]alertKey, alertSilenceDuration time.Duration) {
	var allMessages []string
	var hostIP string

	// Run general monitoring
	if cfg.Monitoring.Enabled {
		messages, ip, err := monitor.General(ctx, cfg.Monitoring, bot)
		if err != nil {
			slog.Error("General monitoring failed", "error", err, "component", "main")
		}
		if len(messages) > 0 {
			allMessages = append(allMessages, messages...)
			hostIP = ip
		}
	}

	// Run RabbitMQ monitoring
	if cfg.RabbitMQ.Enabled {
		messages, ip, err := monitor.RabbitMQ(ctx, cfg.RabbitMQ, bot)
		if err != nil {
			slog.Error("RabbitMQ monitoring failed", "error", err, "component", "main")
		}
		if len(messages) > 0 {
			allMessages = append(allMessages, messages...)
			hostIP = ip
		}
	}

	// Run Redis monitoring
	if cfg.Redis.Enabled {
		messages, ip, err := monitor.Redis(ctx, cfg.Redis, bot)
		if err != nil {
			slog.Error("Redis monitoring failed", "error", err, "component", "main")
		}
		if len(messages) > 0 {
			allMessages = append(allMessages, messages...)
			hostIP = ip
		}
	}

	// Run MySQL monitoring
	if cfg.MySQL.Enabled {
		messages, ip, err := monitor.MySQL(ctx, cfg.MySQL, bot)
		if err != nil {
			slog.Error("MySQL monitoring failed", "error", err, "component", "main")
		}
		if len(messages) > 0 {
			allMessages = append(allMessages, messages...)
			hostIP = ip
		}
	}

	// Run Nacos monitoring
	if cfg.Nacos.Enabled {
		messages, ip, err := monitor.Nacos(ctx, cfg.Nacos, bot)
		if err != nil {
			slog.Error("Nacos monitoring failed", "error", err, "component", "main")
		}
		if len(messages) > 0 {
			allMessages = append(allMessages, messages...)
			hostIP = ip
		}
	}

	// Run host monitoring
	if cfg.HostMonitoring.Enabled {
		messages, ip, err := monitor.Host(ctx, cfg.HostMonitoring, bot)
		if err != nil {
			slog.Error("Host monitoring failed", "error", err, "component", "main")
		}
		if len(messages) > 0 {
			allMessages = append(allMessages, messages...)
			hostIP = ip
		}
	}

	// Run system monitoring
	if cfg.SystemMonitoring.Enabled {
		messages, ip, err := monitor.System(ctx, cfg.SystemMonitoring, bot)
		if err != nil {
			slog.Error("System monitoring failed", "error", err, "component", "main")
		}
		if len(messages) > 0 {
			allMessages = append(allMessages, messages...)
			hostIP = ip
		}
	}

	// Combine and deduplicate alerts
	if len(allMessages) > 0 {
		// Combine details from all messages
		var details strings.Builder
		for i, msg := range allMessages {
			// Extract details by splitting on "*详情*:\n" and taking the second part
			parts := strings.SplitN(msg, "*详情*:\n", 2)
			if len(parts) != 2 {
				slog.Warn("Invalid alert format, skipping", "message", msg, "component", "main")
				continue
			}
			// Extract serviceName and eventName from the message
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
				details.WriteString("\n")
			}
			details.WriteString(fmt.Sprintf("**%s - %s**:\n%s", serviceName, eventName, parts[1]))
		}

		if details.Len() == 0 {
			slog.Info("No valid alert details to send", "component", "main")
			return
		}

		// Deduplicate combined alert
		hash, err := util.MD5Hash(details.String())
		if err != nil {
			slog.Error("Failed to generate hash", "error", err, "component", "main")
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
			}
		}
	} else {
		slog.Debug("No alerts generated", "component", "main")
	}
}