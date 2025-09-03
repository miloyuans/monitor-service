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

	// Initialize global alert bot
	var globalBot *alert.AlertBot
	if cfg.IsAnyMonitoringEnabled() && cfg.Telegram.BotToken != "" && cfg.Telegram.ChatID != 0 {
		globalBot, err = alert.NewAlertBot(cfg.Telegram.BotToken, cfg.Telegram.ChatID, cfg.ClusterName, cfg.ShowHostname)
		if err != nil {
			slog.Error("Failed to initialize global alert bot", "error", err, "component", "main")
			os.Exit(1)
		}
	}

	// Initialize MySQL-specific alert bot
	var mysqlBot *alert.AlertBot
	if cfg.MySQL.Enabled && cfg.MySQL.HasIndependentTelegramConfig() {
		mysqlBot, err = alert.NewAlertBot(cfg.MySQL.Telegram.BotToken, cfg.MySQL.Telegram.ChatID, cfg.MySQL.ClusterName, cfg.ShowHostname)
		if err != nil {
			slog.Error("Failed to initialize MySQL alert bot", "error", err, "component", "main")
			os.Exit(1)
		}
	}

	// Use global bot as fallback for MySQL if no specific bot is configured
	mysqlAlertBot := mysqlBot
	if mysqlBot == nil {
		mysqlAlertBot = globalBot
	}

	// Get private IP for startup and shutdown alerts
	ctx := context.Background()
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP for startup alert", "error", err, "component", "main")
		hostIP = "unknown"
	}

	// Send startup alerts
	if cfg.IsAnyMonitoringEnabled() && globalBot != nil {
		// Only include modules without independent Telegram configurations in global startup alert
		var details strings.Builder
		details.WriteString("监控服务已启动\n启用模块:\n")
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
		startupMsg := globalBot.FormatAlert(fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "启动", details.String(), hostIP, "startup")
		if err := globalBot.SendAlert(ctx, fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "启动", details.String(), hostIP, "startup"); err != nil {
			slog.Error("Failed to send global startup alert", "error", err, "message", startupMsg, "component", "main")
		} else {
			slog.Info("Sent global startup alert", "message", startupMsg, "component", "main")
		}
	}

	// Send MySQL-specific startup alert if independent configuration is used
	if cfg.MySQL.Enabled && mysqlBot != nil {
		startupMsg := mysqlBot.FormatAlert("数据库监控", "启动", "MySQL 监控服务已启动", hostIP, "startup")
		if err := mysqlBot.SendAlert(ctx, "数据库监控", "启动", "MySQL 监控服务已启动", hostIP, "startup"); err != nil {
			slog.Error("Failed to send MySQL startup alert", "error", err, "message", startupMsg, "component", "main")
		} else {
			slog.Info("Sent MySQL startup alert", "message", startupMsg, "component", "main")
		}
	}

	// Log enabled monitoring features
	logMonitoringStatus(cfg)

	// Parse check interval
	interval, err := time.ParseDuration(cfg.CheckInterval)
	if err != nil {
		slog.Error("Invalid check interval", "interval", cfg.CheckInterval, "error", err, "component", "main")
		if globalBot != nil {
			startupMsg := globalBot.FormatAlert(fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "异常", fmt.Sprintf("无效的检查间隔: %v", err), hostIP, "alert")
			if err := globalBot.SendAlert(ctx, fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "异常", fmt.Sprintf("无效的检查间隔: %v", err), hostIP, "alert"); err != nil {
				slog.Error("Failed to send alert for invalid check interval", "error", err, "message", startupMsg, "component", "main")
			}
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
		// Send shutdown alerts
		if globalBot != nil {
			var details strings.Builder
			details.WriteString("监控服务已停止\n已停止模块:\n")
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
			shutdownMsg := globalBot.FormatAlert(fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "停止", details.String(), hostIP, "shutdown")
			if err := globalBot.SendAlert(context.Background(), fmt.Sprintf("Monitor Service %s", cfg.ClusterName), "停止", details.String(), hostIP, "shutdown"); err != nil {
				slog.Error("Failed to send global shutdown alert", "error", err, "message", shutdownMsg, "component", "main")
			} else {
				slog.Info("Sent global shutdown alert", "message", shutdownMsg, "component", "main")
			}
		}
		if cfg.MySQL.Enabled && mysqlBot != nil {
			shutdownMsg := mysqlBot.FormatAlert("数据库监控", "停止", "MySQL 监控服务已停止", hostIP, "shutdown")
			if err := mysqlBot.SendAlert(context.Background(), "数据库监控", "停止", "MySQL 监控服务已停止", hostIP, "shutdown"); err != nil {
				slog.Error("Failed to send MySQL shutdown alert", "error", err, "message", shutdownMsg, "component", "main")
			} else {
				slog.Info("Sent MySQL shutdown alert", "message", shutdownMsg, "component", "main")
			}
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
			monitorAndAlert(ctx, cfg, globalBot, mysqlAlertBot, alertCache, &cacheMutex, alertSilenceDuration, interval)
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
func monitorAndAlert(ctx context.Context, cfg config.Config, globalBot, mysqlBot *alert.AlertBot, alertCache map[string]map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, checkInterval time.Duration) {
	var wg sync.WaitGroup

	// Define monitoring tasks
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
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.General(ctx, cfg.(config.GeneralConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.Monitoring,
		},
		{
			name:    "RabbitMQ",
			enabled: cfg.RabbitMQ.Enabled,
			bot:     globalBot,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.RabbitMQ(ctx, cfg.(config.RabbitMQConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.RabbitMQ,
		},
		{
			name:    "Redis",
			enabled: cfg.Redis.Enabled,
			bot:     globalBot,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.Redis(ctx, cfg.(config.RedisConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.Redis,
		},
		{
			name:    "MySQL",
			enabled: cfg.MySQL.Enabled,
			bot:     mysqlBot,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.MySQL(ctx, cfg.(config.MySQLConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.MySQL,
		},
		{
			name:    "Nacos",
			enabled: cfg.Nacos.Enabled,
			bot:     globalBot,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.Nacos(ctx, cfg.(config.NacosConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.Nacos,
		},
		{
			name:    "Host",
			enabled: cfg.HostMonitoring.Enabled,
			bot:     globalBot,
			fn:      func(ctx context.Context, cfg interface{}, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.Host(ctx, cfg.(config.HostConfig), bot, cache, mutex, duration)
			},
			cfg:     cfg.HostMonitoring,
		},
		{
			name:    "System",
			enabled: cfg.SystemMonitoring.Enabled,
			bot:     globalBot,
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
		go func(name string, fn func(context.Context, interface{}, *alert.AlertBot, map[string]time.Time, *sync.Mutex, time.Duration) error, cfg interface{}, bot *alert.AlertBot) {
			defer wg.Done()
			// Create a task-specific context with timeout
			taskCtx, taskCancel := context.WithTimeout(ctx, checkInterval)
			defer taskCancel()

			cacheMutex.Lock()
			cache := alertCache[name]
			cacheMutex.Unlock()

			if err := fn(taskCtx, cfg, bot, cache, cacheMutex, alertSilenceDuration); err != nil {
				// Handle expected issues (e.g., user/process changes in System) differently
				if name == "System" && strings.Contains(err.Error(), "system issues detected") {
					slog.Info("System monitor detected expected issues (e.g., user or process changes)", "monitor", name, "error", err, "component", "main")
				} else {
					slog.Error("Monitoring task failed", "monitor", name, "error", err, "component", "main")
				}
			} else {
				slog.Debug("Monitoring task completed successfully", "monitor", name, "component", "main")
			}
		}(m.name, m.fn, m.cfg, m.bot)
	}
	wg.Wait()
}