package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/monitor"
	"monitor-service/util"
	"golang.org/x/net/context"
)

var (
	alertLastSent   sync.Map
	silenceDuration time.Duration
	checkInterval   time.Duration
	ctx             context.Context
	cancel          context.CancelFunc
)

func main() {
	// Initialize context with cancellation support.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	// Load configuration.
	cfg, err := config.LoadConfig("/app/config.yaml")
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	if !cfg.Monitoring.Enabled {
		fmt.Println("Monitoring is disabled.")
		os.Exit(0)
	}

	// Parse duration settings.
	silenceDuration = time.Duration(cfg.AlertSilenceDuration) * time.Minute
	checkInterval, err = time.ParseDuration(cfg.CheckInterval)
	if err != nil {
		fmt.Printf("Invalid check_interval: %v\n", err)
		os.Exit(1)
	}

	// Initialize alert system (Telegram bot).
	alertBot, err := alert.NewAlertBot(cfg.Telegram.BotToken, cfg.Telegram.ChatID, cfg.ClusterName, cfg.ShowHostname)
	if err != nil {
		fmt.Printf("Error creating Telegram bot: %v\n", err)
		os.Exit(1)
	}

	// Start HTTP server for health checks.
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "OK")
		})
		if err := http.ListenAndServe(":8080", nil); err != nil {
			fmt.Printf("Error starting HTTP server: %v\n", err)
			os.Exit(1)
		}
	}()

	// Check if any monitoring is enabled.
	anyMonitoringEnabled := cfg.RabbitMQ.Enabled || cfg.Redis.Enabled || cfg.MySQL.Enabled || cfg.Nacos.Enabled || cfg.HostMonitoring.Enabled || cfg.SystemMonitoring.Enabled

	// Start monitoring loop.
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !anyMonitoringEnabled {
				fmt.Println("无所事事")
				continue
			}
			messages := []string{}
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
				if msgs, err := monitor.System(ctx, cfg.SystemMonitoring, cfg.ClusterName); err != nil {
					messages = append(messages, msgs...)
				}
			}

			if len(messages) > 0 {
				alertMsg := strings.Join(messages, "\n\n")
				hash, err := util.MD5Hash(alertMsg)
				if err != nil {
					fmt.Printf("Error hashing alert message: %v\n", err)
					continue
				}
				last, ok := alertLastSent.Load(hash)
				if !ok || time.Since(last.(time.Time)) > silenceDuration {
					alertBot.SendAlert(alertMsg)
					alertLastSent.Store(hash, time.Now())
				}
			}
		case <-ctx.Done():
			fmt.Println("Shutting down monitoring loop")
			return
		}
	}
}