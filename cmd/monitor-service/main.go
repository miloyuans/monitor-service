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

	// Parse retry delay and check interval
	retryDelay, err := time.ParseDuration(cfg.RetryDelay)
	if err != nil {
		slog.Error("Invalid retry delay", "retry_delay", cfg.RetryDelay, "error", err, "component", "main")
		os.Exit(1)
	}
	interval, err := time.ParseDuration(cfg.CheckInterval)
	if err != nil {
		slog.Error("Invalid check interval", "interval", cfg.CheckInterval, "error", err, "component", "main")
		os.Exit(1)
	}
	alertSilenceDuration := time.Duration(cfg.AlertSilenceDuration) * time.Minute

	// Initialize alert cache
	alertCache := make(map[string]time.Time)
	var cacheMutex sync.Mutex

	// Define destinations
	destinations := []string{"telegram"}
	if cfg.MonitorWebURL != "" {
		destinations = append(destinations, "web")
	}

	// Initialize alert bots
	bots := make(map[string]*alert.AlertBot)
	if cfg.IsAnyMonitoringEnabled() && (cfg.Telegram.BotToken != "" || cfg.MonitorWebURL != "") {
		bots["global"], err = alert.NewAlertBot(cfg.Telegram.BotToken, cfg.Telegram.ChatID, cfg.ClusterName, cfg.ShowHostname, cfg.MonitorWebURL, cfg.RetryTimes, retryDelay)
		if err != nil {
			slog.Error("Failed to initialize global alert bot", "error", err, "component", "main")
			os.Exit(1)
		}
		if err := bots["global"].LoadAndRetryUnsentAlerts(context.Background()); err != nil {
			slog.Warn("Failed to retry unsent alerts for global bot", "error", err, "component", "main")
		}
	}
	if cfg.MySQL.Enabled && cfg.MySQL.HasIndependentTelegramConfig() {
		bots["mysql"], err = alert.NewAlertBot(cfg.MySQL.Telegram.BotToken, cfg.MySQL.Telegram.ChatID, cfg.MySQL.ClusterName, cfg.ShowHostname, cfg.MonitorWebURL, cfg.RetryTimes, retryDelay)
		if err != nil {
			slog.Error("Failed to initialize MySQL alert bot", "error", err, "component", "main")
			os.Exit(1)
		}
		if err := bots["mysql"].LoadAndRetryUnsentAlerts(context.Background()); err != nil {
			slog.Warn("Failed to retry unsent alerts for MySQL bot", "error", err, "component", "main")
		}
	}
	mysqlAlertBot := bots["mysql"]
	if mysqlAlertBot == nil {
		mysqlAlertBot = bots["global"]
	}

	// Get private IP
	ctx := context.Background()
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "main")
		hostIP = "unknown"
	}

	// Send startup alerts
	if err := sendStartupShutdownAlert(ctx, cfg, bots, hostIP, "startup", alertCache, &cacheMutex, alertSilenceDuration, destinations); err != nil {
		slog.Error("Failed to send startup alerts", "error", err, "component", "main")
	}

	// Log monitoring status
	logMonitoringStatus(cfg)

	// Set up signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		<-ctx.Done()
		slog.Info("Received signal, initiating shutdown", "signal", ctx.Err(), "component", "main")
		if err := sendStartupShutdownAlert(context.Background(), cfg, bots, hostIP, "shutdown", alertCache, &cacheMutex, alertSilenceDuration, destinations); err != nil {
			slog.Error("Failed to send shutdown alerts", "error", err, "component", "main")
		}
	}()

	// Start monitoring
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Monitoring stopped gracefully", "component", "main")
			return
		case <-ticker.C:
			monitorAndAlert(ctx, cfg, bots, mysqlAlertBot, alertCache, &cacheMutex, alertSilenceDuration, interval)
		}
	}
}

// sendStartupShutdownAlert sends startup or shutdown alerts for global and MySQL bots with deduplication.
func sendStartupShutdownAlert(ctx context.Context, cfg config.Config, bots map[string]*alert.AlertBot, hostIP, alertType string, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, destinations []string) error {
	var errors []error
	alerts := []struct {
		bot         *alert.AlertBot
		serviceName string
		details     string
		module      string
		alertKey    string
	}{
		{
			bot:         bots["global"],
			serviceName: fmt.Sprintf("Monitor Service %s", cfg.ClusterName),
			details:     buildAlertDetails(cfg, alertType, false),
			module:      "general",
			alertKey:    fmt.Sprintf("global_%s_%s", alertType, hostIP),
		},
		{
			bot:         bots["mysql"],
			serviceName: "数据库监控",
			details:     fmt.Sprintf("MySQL 监控服务已%s", map[bool]string{true: "启动", false: "停止"}[alertType == "startup"]),
			module:      "mysql",
			alertKey:    fmt.Sprintf("mysql_%s_%s", alertType, hostIP),
		},
	}

	for _, a := range alerts {
		if a.bot == nil || (a.module == "mysql" && !cfg.MySQL.Enabled) {
			continue
		}
		cacheMutex.Lock()
		if lastAlertTime, exists := alertCache[a.alertKey]; exists && time.Since(lastAlertTime) < alertSilenceDuration {
			slog.Debug("Skipping alert due to silence duration", "module", a.module, "alert_type", alertType, "host_ip", hostIP, "last_alert", lastAlertTime, "component", "main")
			cacheMutex.Unlock()
			continue
		}
		alertCache[a.alertKey] = time.Now()
		cacheMutex.Unlock()

		startupMsg := a.bot.FormatAlert(a.serviceName, alertType, a.details, hostIP, alertType)
		if err := a.bot.SendAlert(ctx, a.serviceName, alertType, a.details, hostIP, alertType, a.module, map[string]interface{}{"destinations": destinations, "alert_key": a.alertKey}); err != nil {
			slog.Error("Failed to send alert", "module", a.module, "alert_type", alertType, "error", err, "message", startupMsg, "component", "main")
			errors = append(errors, err)
		} else {
			slog.Info("Sent alert", "module", a.module, "alert_type", alertType, "destinations", destinations, "message", startupMsg, "component", "main")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to send %s alerts: %v", alertType, errors)
	}
	return nil
}

// buildAlertDetails constructs the details string for global startup/shutdown alerts.
func buildAlertDetails(cfg config.Config, alertType string, isMySQLIndependent bool) string {
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
	if cfg.MySQL.Enabled && !isMySQLIndependent {
		enabledModules = append(enabledModules, "MySQL")
	}
	for _, module := range enabledModules {
		fmt.Fprintf(&details, "- %s\n", module)
	}
	if cfg.MySQL.Enabled && isMySQLIndependent {
		details.WriteString("MySQL 使用独立 Telegram 通知\n")
	}
	return details.String()
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
			slog.Debug("Monitoring feature disabled", "feature", feature.name, "component", "main")
		}
	}
}

// monitorAndAlert runs monitoring tasks concurrently with timeout and semaphore.
func monitorAndAlert(ctx context.Context, cfg config.Config, bots map[string]*alert.AlertBot, mysqlBot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration, checkInterval time.Duration) {
	const maxWorkers = 5
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	var errors []error
	var errMutex sync.Mutex

	monitors := []struct {
		name    string
		enabled bool
		bot     *alert.AlertBot
		cacheKey string
		fn      func(context.Context, *alert.AlertBot, map[string]time.Time, *sync.Mutex, time.Duration) error
	}{
		{
			name:     "General",
			enabled:  cfg.Monitoring.Enabled,
			bot:      bots["global"],
			cacheKey: "General",
			fn: func(ctx context.Context, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.General(ctx, cfg.Monitoring, bot, cache, mutex, duration)
			},
		},
		{
			name:     "RabbitMQ",
			enabled:  cfg.RabbitMQ.Enabled,
			bot:      bots["global"],
			cacheKey: "RabbitMQ",
			fn: func(ctx context.Context, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.RabbitMQ(ctx, cfg.RabbitMQ, bot, cache, mutex, duration)
			},
		},
		{
			name:     "Redis",
			enabled:  cfg.Redis.Enabled,
			bot:      bots["global"],
			cacheKey: "Redis",
			fn: func(ctx context.Context, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.Redis(ctx, cfg.Redis, bot, cache, mutex, duration)
			},
		},
		{
			name:     "MySQL",
			enabled:  cfg.MySQL.Enabled,
			bot:      mysqlBot,
			cacheKey: "MySQL",
			fn: func(ctx context.Context, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.MySQL(ctx, cfg.MySQL, bot, cache, mutex, duration)
			},
		},
		{
			name:     "Nacos",
			enabled:  cfg.Nacos.Enabled,
			bot:      bots["global"],
			cacheKey: "Nacos",
			fn: func(ctx context.Context, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.Nacos(ctx, cfg.Nacos, bot, cache, mutex, duration)
			},
		},
		{
			name:     "Host",
			enabled:  cfg.HostMonitoring.Enabled,
			bot:      bots["global"],
			cacheKey: "Host",
			fn: func(ctx context.Context, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.Host(ctx, cfg.HostMonitoring, bot, cache, mutex, duration)
			},
		},
		{
			name:     "System",
			enabled:  cfg.SystemMonitoring.Enabled,
			bot:      bots["global"],
			cacheKey: "System",
			fn: func(ctx context.Context, bot *alert.AlertBot, cache map[string]time.Time, mutex *sync.Mutex, duration time.Duration) error {
				return monitor.System(ctx, cfg.SystemMonitoring, bot, cache, mutex, duration)
			},
		},
	}

	// Run monitoring tasks concurrently
	for _, m := range monitors {
		if !m.enabled {
			slog.Debug("Skipping disabled monitoring task", "module", m.name, "component", "main")
			continue
		}

		select {
		case <-ctx.Done():
			slog.Info("Monitoring cycle aborted due to context cancellation", "module", m.name, "error", ctx.Err(), "component", "main")
			return
		case sem <- struct{}{}:
			wg.Add(1)
			go func(name, cacheKey string, fn func(context.Context, *alert.AlertBot, map[string]time.Time, *sync.Mutex, time.Duration) error, bot *alert.AlertBot) {
				defer wg.Done()
				defer func() { <-sem }()

				taskCtx, taskCancel := context.WithTimeout(ctx, checkInterval)
				defer taskCancel()

				if err := fn(taskCtx, bot, alertCache, cacheMutex, alertSilenceDuration); err != nil {
					if name == "System" && strings.Contains(err.Error(), "system issues detected") {
						slog.Info("System monitor detected expected issues", "module", name, "error", err, "component", "main")
					} else {
						errMutex.Lock()
						errors = append(errors, fmt.Errorf("%s: %w", name, err))
						errMutex.Unlock()
						slog.Error("Monitoring task failed", "module", name, "error", err, "component", "main")
					}
				} else {
					slog.Debug("Monitoring task completed successfully", "module", name, "component", "main")
				}
			}(m.name, m.cacheKey, m.fn, m.bot)
		}
	}
	wg.Wait()

	if len(errors) > 0 {
		slog.Error("Monitoring cycle completed with errors", "errors", errors, "component", "main")
	} else {
		slog.Debug("Monitoring cycle completed successfully", "component", "main")
	}
}