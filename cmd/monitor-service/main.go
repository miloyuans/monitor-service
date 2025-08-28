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

	// Start monitoring loop
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !anyMonitoringEnabled {
				logger.Info("无所事事")
				continue
			}
			messages := []string{}
			var systemIP string
			if cfg.RabbitMQ.Enabled {
				if msg, err := monitor.RabbitMQ(ctx, cfg.RabbitMQ, cfg.ClusterName); err != nil {
					messages = append(messages, msg)
				}
			}
			if cfg.Redis.Enabled {
				if msgs, err := monitor.Redis(ctx, cfg.Redis, cfg.ClusterName); err != nil {
					messages = append(messages, msgs...)
				}
			}
			if cfg.MySQL.Enabled {
				if msgs, err := monitor.MySQL(ctx, cfg.MySQL, cfg.ClusterName); err != nil {
					messages = append(messages, msgs...)
				}
			}
			if cfg.Nacos.Enabled {
				if msg, err := monitor.Nacos(ctx, cfg.Nacos, cfg.ClusterName); err != nil {
					messages = append(messages, msg)
				}
			}
			if cfg.HostMonitoring.Enabled {
				if msgs, err := monitor.Host(ctx, cfg.HostMonitoring, cfg.ClusterName); err != nil {
					messages = append(messages, msgs...)
				}
			}
			if cfg.SystemMonitoring.Enabled {
				if msgs, ip, err := monitor.System(ctx, cfg.SystemMonitoring, alertBot); err != nil {
					messages = append(messages, msgs...)
					systemIP = ip
				}
			}

			if len(messages) > 0 {
				alertMsg := strings.Join(messages, "\n\n")
				hash, err := util.MD5Hash(alertMsg)
				if err != nil {
					logger.Error("Error hashing alert message", "error", err)
					continue
				}
				last, ok := alertLastSent.Load(hash)
				if !ok || time.Since(last.(time.Time)) > silenceDuration {
					ip := "unknown"
					if systemIP != "" {
						ip = systemIP
					}
					alertBot.SendAlert(alertMsg, ip)
					alertLastSent.Store(hash, time.Now())
				}
			}
		case <-ctx.Done():
			logger.Info("Shutting down monitoring loop")
			return
		}
	}
}