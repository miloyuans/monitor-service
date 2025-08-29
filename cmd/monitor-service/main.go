package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/monitor"
	"monitor-service/util"
)

var (
	alertLastSent   sync.Map
	silenceDuration time.Duration
	checkInterval   time.Duration
	ctx             context.Context
	cancel          context.CancelFunc
	logger          *slog.Logger
)

func main() {
	// Initialize logger with JSON handler for Log4j-style output
	logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		Level:     slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Initialize context with cancellation support
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	// Load configuration
	cfg, err := config.LoadConfig("/app/config.yaml")
	if err != nil {
		logger.Error("Error loading config", "error", err)
		os.Exit(1)
	}

	if !cfg.Monitoring.Enabled {
		logger.Info("Monitoring is disabled")
		os.Exit(0)
	}

	// Parse duration settings
	silenceDuration = time.Duration(cfg.AlertSilenceDuration) * time.Minute
	checkInterval, err = time.ParseDuration(cfg.CheckInterval)
	if err != nil {
		logger.Error("Invalid check_interval", "error", err)
		os.Exit(1)
	}

	// Initialize alert system (Telegram bot)
	alertBot, err := alert.NewAlertBot(cfg.Telegram.BotToken, cfg.Telegram.ChatID, cfg.ClusterName, cfg.ShowHostname)
	if err != nil {
		logger.Error("Error creating Telegram bot", "error", err)
		os.Exit(1)
	}

	// Check if any monitoring is enabled
	anyMonitoringEnabled := cfg.RabbitMQ.Enabled || cfg.Redis.Enabled || cfg.MySQL.Enabled || cfg.Nacos.Enabled || cfg.HostMonitoring.Enabled || cfg.SystemMonitoring.Enabled
	if !anyMonitoringEnabled {
		logger.Info("No monitoring enabled, exiting")
		os.Exit(0)
	}

	// Start monitoring loop
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			monitorAndAlert(ctx, cfg, alertBot)
		case <-ctx.Done():
			logger.Info("Shutting down monitoring loop")
			return
		}
	}
}

// monitorAndAlert runs all enabled monitors concurrently and sends alerts.
func monitorAndAlert(ctx context.Context, cfg *config.Config, alertBot *alert.AlertBot) {
	var wg sync.WaitGroup
	messages := make([]string, 0)
	var systemIP string
	mu := sync.Mutex{} // Protect messages and systemIP

	// Helper to append messages and update IP safely
	appendResult := func(msgs []string, ip string, err error) {
		if err != nil {
			mu.Lock()
			messages = append(messages, msgs...)
			if ip != "" && systemIP == "" {
				systemIP = ip
			}
			mu.Unlock()
		}
	}

	// Run monitors concurrently
	if cfg.RabbitMQ.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if msg, err := monitor.RabbitMQ(ctx, cfg.RabbitMQ, cfg.ClusterName); err != nil {
				logger.Warn("RabbitMQ monitoring error", "error", err)
				appendResult([]string{msg}, "", err)
			}
		}()
	}
	if cfg.Redis.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if msgs, err := monitor.Redis(ctx, cfg.Redis, cfg.ClusterName); err != nil {
				logger.Warn("Redis monitoring error", "error", err)
				appendResult(msgs, "", err)
			}
		}()
	}
	if cfg.MySQL.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if msgs, err := monitor.MySQL(ctx, cfg.MySQL, cfg.ClusterName); err != nil {
				logger.Warn("MySQL monitoring error", "error", err)
				appendResult(msgs, "", err)
			}
		}()
	}
	if cfg.Nacos.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if msg, err := monitor.Nacos(ctx, cfg.Nacos, cfg.ClusterName); err != nil {
				logger.Warn("Nacos monitoring error", "error", err)
				appendResult([]string{msg}, "", err)
			}
		}()
	}
	if cfg.HostMonitoring.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if msgs, ip, err := monitor.Host(ctx, cfg.HostMonitoring, alertBot); err != nil {
				logger.Warn("Host monitoring error", "error", err)
				appendResult(msgs, ip, err)
			}
		}()
	}
	if cfg.SystemMonitoring.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if msgs, ip, err := monitor.System(ctx, cfg.SystemMonitoring, alertBot); err != nil {
				logger.Warn("System monitoring error", "error", err)
				appendResult(msgs, ip, err)
			}
		}()
	}

	// Wait for all monitors to complete
	wg.Wait()

	// Clean up old alertLastSent entries (older than 2 * silenceDuration)
	alertLastSent.Range(func(key, value any) bool {
		if time.Since(value.(time.Time)) > 2*silenceDuration {
			alertLastSent.Delete(key)
		}
		return true
	})

	// Send alerts if any
	if len(messages) > 0 {
		alertMsg := strings.Join(messages, "\n\n")
		hash, err := util.MD5Hash(alertMsg)
		if err != nil {
			logger.Error("Error hashing alert message", "error", err)
			return
		}
		last, ok := alertLastSent.Load(hash)
		if !ok || time.Since(last.(time.Time)) > silenceDuration {
			ip := systemIP
			if ip == "" {
				ip = "unknown"
			}
			alertBot.SendAlert(alertMsg, ip)
			alertLastSent.Store(hash, time.Now())
			logger.Info("Sent alert", "message_count", len(messages), "ip", ip)
		} else {
			logger.Debug("Alert suppressed due to deduplication", "hash", hash)
		}
	} else {
		logger.Info("No issues detected in this monitoring cycle")
	}
}