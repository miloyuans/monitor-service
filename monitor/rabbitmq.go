package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rabbitmq/amqp091-go"
	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/util"
)

// RabbitMQ monitors RabbitMQ instance and sends alerts if issues are detected.
func RabbitMQ(ctx context.Context, cfg config.RabbitMQConfig, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration) error {
	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "rabbitmq")
		hostIP = "unknown"
	}

	// Initialize details for alert message
	var details strings.Builder
	hasIssue := false

	// Connect to RabbitMQ
	conn, err := amqp091.DialConfig(cfg.URL, amqp091.Config{
		Dial:      amqp091.DefaultDial(time.Second * 5),
		Heartbeat: 10 * time.Second,
	})
	if err != nil {
		slog.Error("Failed to connect to RabbitMQ", "url", cfg.URL, "error", err, "component", "rabbitmq")
		details.WriteString(fmt.Sprintf("无法连接到 RabbitMQ: %v", err))
		msg := bot.FormatAlert("RabbitMQ告警", "服务异常", details.String(), hostIP, "alert")
		return sendRabbitMQAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "RabbitMQ告警", "服务异常", details.String(), hostIP, "alert", msg)
	}
	defer conn.Close()

	// Open a channel
	ch, err := conn.Channel()
	if err != nil {
		slog.Error("Failed to open RabbitMQ channel", "error", err, "component", "rabbitmq")
		details.WriteString(fmt.Sprintf("无法打开 RabbitMQ 通道: %v", err))
		msg := bot.FormatAlert("RabbitMQ告警", "服务异常", details.String(), hostIP, "alert")
		return sendRabbitMQAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "RabbitMQ告警", "服务异常", details.String(), hostIP, "alert", msg)
	}
	defer ch.Close()

	// Check queue status using management API
	queues, err := listQueues(ctx, cfg)
	if err != nil {
		slog.Error("Failed to list RabbitMQ queues", "error", err, "component", "rabbitmq")
		details.WriteString(fmt.Sprintf("无法获取队列列表: %v", err))
		msg := bot.FormatAlert("RabbitMQ告警", "服务异常", details.String(), hostIP, "alert")
		return sendRabbitMQAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "RabbitMQ告警", "服务异常", details.String(), hostIP, "alert", msg)
	}

	// Monitor queue metrics
	const maxMessages = 1000 // Threshold for messages in a queue
	for _, queue := range queues {
		if queue.Messages > maxMessages {
			hasIssue = true
			fmt.Fprintf(&details, "**队列 %s**:\n消息数: %d (超过阈值 %d)\n", alert.EscapeMarkdown(queue.Name), queue.Messages, maxMessages)
		}
	}

	// Check connection and channel status
	connStatus := "正常✅"
	if conn.IsClosed() {
		connStatus = "异常❌ 连接已关闭"
		hasIssue = true
	}
	fmt.Fprintf(&details, "**连接状态**: %s\n", alert.EscapeMarkdown(connStatus))

	if hasIssue {
		slog.Info("RabbitMQ issues detected", "queues", len(queues), "component", "rabbitmq")
		msg := bot.FormatAlert("RabbitMQ告警", "服务异常", details.String(), hostIP, "alert")
		return sendRabbitMQAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "RabbitMQ告警", "服务异常", details.String(), hostIP, "alert", msg)
	}

	slog.Debug("No RabbitMQ issues detected", "component", "rabbitmq")
	return nil
}

// sendRabbitMQAlert sends a deduplicated Telegram alert for the RabbitMQ module.
func sendRabbitMQAlert(ctx context.Context, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, serviceName, eventName, details, hostIP, alertType, message string) error {
	hash, err := util.MD5Hash(details)
	if err != nil {
		slog.Error("Failed to generate alert hash", "error", err, "component", "rabbitmq")
		return fmt.Errorf("failed to generate alert hash: %w", err)
	}
	cacheMutex.Lock()
	now := time.Now()
	if timestamp, ok := alertCache[hash]; ok && now.Sub(timestamp) < alertSilenceDuration {
		slog.Info("Skipping duplicate alert", "hash", hash, "component", "rabbitmq")
		cacheMutex.Unlock()
		return nil
	}
	alertCache[hash] = now
	// Clean up old cache entries
	for h, t := range alertCache {
		if now.Sub(t) >= alertSilenceDuration {
			delete(alertCache, h)
			slog.Debug("Removed expired alert cache entry", "hash", h, "component", "rabbitmq")
		}
	}
	cacheMutex.Unlock()
	slog.Debug("Sending alert", "message", message, "component", "rabbitmq")
	if err := bot.SendAlert(ctx, serviceName, eventName, details, hostIP, alertType); err != nil {
		slog.Error("Failed to send alert", "error", err, "component", "rabbitmq")
		return fmt.Errorf("failed to send alert: %w", err)
	}
	slog.Info("Sent alert", "message", message, "component", "rabbitmq")
	return nil
}

// QueueInfo holds queue details.
type QueueInfo struct {
	Name     string `json:"name"`
	Messages int    `json:"messages"`
}

// listQueues retrieves all RabbitMQ queues and their details using the management API.
func listQueues(ctx context.Context, cfg config.RabbitMQConfig) ([]QueueInfo, error) {
	// Use management API address from config
	url := fmt.Sprintf("%s/api/queues", cfg.Address)
	if cfg.Address == "" {
		return nil, fmt.Errorf("rabbitmq.address is empty")
	}

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Create request with basic auth
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.SetBasicAuth(cfg.Username, cfg.Password)

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query RabbitMQ management API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code from RabbitMQ management API: %d", resp.StatusCode)
	}

	// Parse response
	var queues []QueueInfo
	if err := json.NewDecoder(resp.Body).Decode(&queues); err != nil {
		return nil, fmt.Errorf("failed to decode RabbitMQ queues: %w", err)
	}

	return queues, nil
}