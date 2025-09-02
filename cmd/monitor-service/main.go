package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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

	// Alert deduplication cache per module
	alertCache := make(map[string]map[string]time.Time) // Map of module name to alert hash and timestamp
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

// monitorAndAlert runs monitoring tasks concurrently.
func monitorAndAlert(ctx context.Context, cfg config.Config, bot *alert.AlertBot, alertCache map[string]map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, checkInterval time.Duration) {
	var wg sync.WaitGroup

	// Define monitoring tasks
	monitors := []struct {
		name string
		enabled bool
		fn      func(context.Context, interface{}, *alert.AlertBot, map[string]time.Time, *sync.Mutex, time.Duration) error
		cfg     interface{}
	}{
		{
			name:    "General",
			enabled: cfg.Monitoring.Enabled,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.General(ctx, cfg.(config.GeneralConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.Monitoring,
		},
		{
			name:    "RabbitMQ",
			enabled: cfg.RabbitMQ.Enabled,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.RabbitMQ(ctx, cfg.(config.RabbitMQConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.RabbitMQ,
		},
		{
			name:    "Redis",
			enabled: cfg.Redis.Enabled,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.Redis(ctx, cfg.(config.RedisConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.Redis,
		},
		{
			name:    "MySQL",
			enabled: cfg.MySQL.Enabled,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.MySQL(ctx, cfg.(config.MySQLConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.MySQL,
		},
		{
			name:    "Nacos",
			enabled: cfg.Nacos.Enabled,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.Nacos(ctx, cfg.(config.NacosConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.Nacos,
		},
		{
			name:    "Host",
			enabled: cfg.HostMonitoring.Enabled,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.Host(ctx, cfg.(config.HostConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.HostMonitoring,
		},
		{
			name:    "System",
			enabled: cfg.SystemMonitoring.Enabled,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.System(ctx, cfg.(config.SystemConfig), bot, cache, mutex, duration)
			},
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

	// Run enabled monitoring tasks concurrently
	for _, m := range monitors {
		if !m.enabled {
			slog.Debug("Skipping disabled monitoring task", "monitor", m.name, "component", "main")
			continue
		}

		wg.Add(1)
		go func(name string, fn func(context.Context, interface{}, *alert.AlertBot, map[string]time.Time, *sync.Mutex, time.Duration) error, cfg interface{}) {
			defer wg.Done()
			// Create a task-specific context with timeout
			taskCtx, taskCancel := context.WithTimeout(ctx, checkInterval)
			defer taskCancel()

			cacheMutex.Lock()
			cache := alertCache[name]
			cacheMutex.Unlock()

			if err := fn(taskCtx, cfg, bot, cache, cacheMutex, alertSilenceDuration); err != nil {
				slog.Error("Monitoring task failed", "monitor", name, "error", err, "component", "main")
			}
		}(m.name, m.fn, m.cfg)
	}
	wg.Wait()
}