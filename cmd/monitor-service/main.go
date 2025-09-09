package main

import (
	"context"
	"flag"
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

func main() {
	// Initialize logger with JSON handler
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Parse command-line flags
	configPath := flag.String("config", ".config.yaml", "Path to configuration file")
	flag.Parse()

	// Load configuration
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		slog.Error("Failed to load configuration", "error", err, "path", *configPath, "component", "main")
		os.Exit(1)
	}

	// Parse retry delay
	retryDelay, err := time.ParseDuration(cfg.RetryDelay)
	if err != nil {
		slog.Error("Invalid retry delay", "retry_delay", cfg.RetryDelay, "error", err, "component", "main")
		os.Exit(1)
	}

	// Initialize global alert bot
	var globalBot *alert.AlertBot
	if cfg.IsAnyMonitoringEnabled() && (cfg.Telegram.BotToken != "" && cfg.Telegram.ChatID != 0 || cfg.MonitorWebURL != "") {
		globalBot, err = alert.NewAlertBot(cfg.Telegram.BotToken, cfg.Telegram.ChatID, cfg.ClusterName, cfg.ShowHostname, cfg.MonitorWebURL, cfg.RetryTimes, retryDelay)
		if err != nil {
			slog.Error("Failed to initialize global alert bot", "error", err, "component", "main")
			os.Exit(1)
		}
		if err := globalBot.LoadAndRetryUnsentAlerts(context.Background()); err != nil {
			slog.Error("Failed to retry unsent alerts for global bot", "error", err, "component", "main")
		}
	}

	// Initialize MySQL-specific alert bot
	var mysqlBot *alert.AlertBot
	if cfg.MySQL.Enabled && cfg.MySQL.HasIndependentTelegramConfig() {
		mysqlBot, err = alert.NewAlertBot(cfg.MySQL.Telegram.BotToken, cfg.MySQL.Telegram.ChatID, cfg.MySQL.ClusterName, cfg.ShowHostname, cfg.MonitorWebURL, cfg.RetryTimes, retryDelay)
		if err != nil {
			slog.Error("Failed to initialize MySQL alert bot", "error", err, "component", "main")
			os.Exit(1)
		}
		if err := mysqlBot.LoadAndRetryUnsentAlerts(context.Background()); err != nil {
			slog.Error("Failed to retry unsent alerts for MySQL bot", "error", err, "component", "main")
		}
	}

	// Use global bot as fallback for MySQL
	mysqlAlertBot := mysqlBot
	if mysqlBot == nil {
		mysqlAlertBot = globalBot
	}

	// Get private IP
	ctx := context.Background()
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "main")
		hostIP = "unknown"
	}

	// Send startup alerts
	if err := sendStartupShutdownAlert(ctx, cfg, globalBot, mysqlBot, hostIP, "startup"); err != nil {
		slog.Error("Failed to send startup alerts", "error", err, "component", "main")
	}

	// Log monitoring status
	logMonitoringStatus(cfg)

	// Parse check interval
	interval, err := time.ParseDuration(cfg.CheckInterval)
	if err != nil {
		slog.Error("Invalid check interval", "interval", cfg.CheckInterval, "error", err, "component", "main")
		if globalBot != nil {
			startupMsg := globalBot.FormatAlert(fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "异常", fmt.Sprintf("无效的检查间隔: %v", err), hostIP, "alert")
			if err := globalBot.SendAlert(ctx, fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "异常", fmt.Sprintf("无效的检查间隔: %v", err), hostIP, "alert", "general", nil); err != nil {
				slog.Error("Failed to send alert for invalid check interval", "error", err, "message", startupMsg, "component", "main")
			}
		}
		os.Exit(1)
	}

	// Set up signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		<-ctx.Done()
		slog.Info("Received signal, initiating shutdown", "signal", ctx.Err(), "component", "main")
		if err := sendStartupShutdownAlert(context.Background(), cfg, globalBot, mysqlBot, hostIP, "shutdown"); err != nil {
			slog.Error("Failed to send shutdown alerts", "error", err, "component", "main")
		}
	}()

	// Start monitoring
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	alertCache := make(map[string]map[string]time.Time)
	var cacheMutex sync.Mutex
	alertSilenceDuration := time.Duration(cfg.AlertSilenceDuration) * time.Minute

	for {
		select {
		case <-ctx.Done():
			slog.Info("Monitoring stopped gracefully", "component", "main")
			return
		case <-ticker.C:
			monitorAndAlert(ctx, cfg, globalBot, mysqlAlertBot, alertCache, &cacheMutex, alertSilenceDuration, interval)
		}
	}
}

// sendStartupShutdownAlert sends startup or shutdown alerts for global and MySQL bots.
func sendStartupShutdownAlert(ctx context.Context, cfg config.Config, globalBot, mysqlBot *alert.AlertBot, hostIP, alertType string) error {
	var errors []error

	// Global alert
	if cfg.IsAnyMonitoringEnabled() && globalBot != nil {
		var details strings.Builder
		if alertType == "startup" {
			details.WriteString("✅监控服务已启动✅\n启用模块:\n")
		} else {
			details.WriteString("❌监控服务已停止❌\n已停止模块:\n")
		}
		enabledModules := []string{}
		if cfg.Monitoring.Enabled {
			enabledModules = append(enabledModules, "General")
		}
		if cfg.RabbitMQ.Enabled {
			enabledModules = append(enabledModules, "RabbitMQ")
		}
		if cfg.Redis.Enabled {
			enabledModules = append(enabledModules, "Redis")
		}
		if cfg.Nacos.Enabled {
			enabledModules = append(enabledModules, "Nacos")
		}
		if cfg.HostMonitoring.Enabled {
			enabledModules = append(enabledModules, "Host")
		}
		if cfg.SystemMonitoring.Enabled {
			enabledModules = append(enabledModules, "System")
		}
		if cfg.MySQL.Enabled && !cfg.MySQL.HasIndependentTelegramConfig() {
			enabledModules = append(enabledModules, "MySQL")
		}
		for _, module := range enabledModules {
			fmt.Fprintf(&details, "- %s\n", module)
		}
		if cfg.MySQL.Enabled && cfg.MySQL.HasIndependentTelegramConfig() {
			details.WriteString("MySQL 使用独立 Telegram 通知\n")
		}
		startupMsg := globalBot.FormatAlert(fmt.Sprintf("Monitor Service %s", cfg.ClusterName), alertType, details.String(), hostIP, alertType)
		if err := globalBot.SendAlert(ctx, fmt.Sprintf("Monitor Service %s", cfg.ClusterName), alertType, details.String(), hostIP, alertType, "general", nil); err != nil {
			slog.Error(fmt.Sprintf("Failed to send global %s alert", alertType), "error", err, "message", startupMsg, "component", "main")
			errors = append(errors, err)
		} else {
			slog.Info(fmt.Sprintf("Sent global %s alert", alertType), "message", startupMsg, "component", "main")
		}
	}

	// MySQL-specific alert
	if cfg.MySQL.Enabled && mysqlBot != nil {
		message := "MySQL 监控服务已"
		if alertType == "startup" {
			message += "启动"
		} else {
			message += "停止"
		}
		startupMsg := mysqlBot.FormatAlert("数据库监控", alertType, message, hostIP, alertType)
		if err := mysqlBot.SendAlert(ctx, "数据库监控", alertType, message, hostIP, alertType, "mysql", nil); err != nil {
			slog.Error(fmt.Sprintf("Failed to send MySQL %s alert", alertType), "error", err, "message", startupMsg, "component", "main")
			errors = append(errors, err)
		} else {
			slog.Info(fmt.Sprintf("Sent MySQL %s alert", alertType), "message", startupMsg, "component", "main")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to send %s alerts: %v", alertType, errors)
	}
	return nil
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

// monitorAndAlert runs monitoring tasks concurrently with timeout.
func monitorAndAlert(ctx context.Context, cfg config.Config, globalBot, mysqlBot *alert.AlertBot, alertCache map[string]map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration, checkInterval time.Duration) {
	var wg sync.WaitGroup
	var errors []error
	var errMutex sync.Mutex

	monitors := []struct {
		name    string
		enabled bool
		bot     *alert.AlertBot
		fn      func(context.Context, interface{}, *alert.AlertBot, map[string]time.Time, *sync.Mutex, time.Duration) error
		cfg     interface{}
	}{
		{
			name:    "General",
			enabled: cfg.Monitoring.Enabled,
			bot:     globalBot,
			fn:      monitor.General,
			cfg:     cfg.Monitoring,
		},
		{
			name:    "RabbitMQ",
			enabled: cfg.RabbitMQ.Enabled,
			bot:     globalBot,
			fn:      monitor.RabbitMQ,
			cfg:     cfg.RabbitMQ,
		},
		{
			name:    "Redis",
			enabled: cfg.Redis.Enabled,
			bot:     globalBot,
			fn:      monitor.Redis,
			cfg:     cfg.Redis,
		},
		{
			name:    "MySQL",
			enabled: cfg.MySQL.Enabled,
			bot:     mysqlBot,
			fn:      monitor.MySQL,
			cfg:     cfg.MySQL,
		},
		{
			name:    "Nacos",
			enabled: cfg.Nacos.Enabled,
			bot:     globalBot,
			fn:      monitor.Nacos,
			cfg:     cfg.Nacos,
		},
		{
			name:    "Host",
			enabled: cfg.HostMonitoring.Enabled,
			bot:     globalBot,
			fn:      monitor.Host,
			cfg:     cfg.HostMonitoring,
		},
		{
			name:    "System",
			enabled: cfg.SystemMonitoring.Enabled,
			bot:     globalBot,
			fn:      monitor.System,
			cfg:     cfg.SystemMonitoring,
		},
	}

	// Initialize per-module alert caches
	cacheMutex.Lock()
	for _, m := range monitors {
		if m.enabled && alertCache[m.name] == nil {
			alertCache[m.name] = make(map[string]time.Time)
		}
	}
	cacheMutex.Unlock()

	// Run monitoring tasks concurrently
	for _, m := range monitors {
		if !m.enabled {
			slog.Debug("Skipping disabled monitoring task", "monitor", m.name, "component", "main")
			continue
		}

		wg.Add(1)
		go func(name string, fn func(context.Context, interface{}, *alert.AlertBot, map[string]time.Time, *sync.Mutex, time.Duration) error, cfg interface{}, bot *alert.AlertBot) {
			defer wg.Done()
			taskCtx, taskCancel := context.WithTimeout(ctx, checkInterval)
			defer taskCancel()

			cacheMutex.Lock()
			cache := alertCache[name]
			cacheMutex.Unlock()

			if err := fn(taskCtx, cfg, bot, cache, cacheMutex, alertSilenceDuration); err != nil {
				if name == "System" && strings.Contains(err.Error(), "system issues detected") {
					slog.Info("System monitor detected expected issues", "monitor", name, "error", err, "component", "main")
				} else {
					slog.Error("Monitoring task failed", "monitor", name, "error", err, "component", "main")
					errMutex.Lock()
					errors = append(errors, fmt.Errorf("%s: %w", name, err))
					errMutex.Unlock()
				}
			} else {
				slog.Debug("Monitoring task completed successfully", "monitor", name, "component", "main")
			}
		}(m.name, m.fn, m.cfg, m.bot)
	}
	wg.Wait()

	if len(errors) > 0 {
		slog.Error("Monitoring cycle completed with errors", "errors", errors, "component", "main")
	}
}