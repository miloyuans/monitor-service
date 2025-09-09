package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rabbitmq/amqp091-go"
	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/util"
)

// QueueInfo holds queue details.
type QueueInfo struct {
	Name      string `json:"name"`
	Messages  int    `json:"messages"`
	Consumers int    `json:"consumers"`
}

// RabbitMQ monitors RabbitMQ instance and sends alerts if issues are detected.
func RabbitMQ(ctx context.Context, cfg config.RabbitMQConfig, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration) error {
	// Get private IP
	hostIP, err := util.GetPrivateIP()
	if err != nil {
		slog.Warn("Failed to get private IP", "error", err, "component", "rabbitmq")
		hostIP = "unknown"
	}

	// Validate configuration
	if cfg.URL == "" || cfg.Address == "" {
		details := fmt.Sprintf("Invalid RabbitMQ configuration: URL=%s, Address=%s", cfg.URL, cfg.Address)
		slog.Error("Invalid RabbitMQ configuration", "url", cfg.URL, "address", cfg.Address, "component", "rabbitmq")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "RabbitMQ告警", "配置参数异常", details, hostIP, "alert", "rabbitmq", map[string]interface{}{})
	}

	// Validate management API address
	address := cfg.Address
	if !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "https://") {
		address = "http://" + address
	}
	if _, err := url.Parse(address); err != nil {
		details := fmt.Sprintf("Invalid RabbitMQ management API address: %v", err)
		slog.Error("Invalid RabbitMQ address", "address", cfg.Address, "error", err, "component", "rabbitmq")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "RabbitMQ告警", "地址格式异常", details, hostIP, "alert", "rabbitmq", map[string]interface{}{})
	}

	// Initialize details for alert message
	var details strings.Builder
	hasIssue := false

	// Connect to RabbitMQ
	conn, err := amqp091.DialConfig(cfg.URL, amqp091.Config{
		Dial:      amqp091.DefaultDial(5 * time.Second),
		Heartbeat: 10 * time.Second,
	})
	if err != nil {
		slog.Error("Failed to connect to RabbitMQ", "url", cfg.URL, "error", err, "component", "rabbitmq")
		details.WriteString(fmt.Sprintf("无法连接到 RabbitMQ: %v\n", err))
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "RabbitMQ告警", "服务异常", details.String(), hostIP, "alert", "rabbitmq", map[string]interface{}{"error": err.Error()})
	}
	defer conn.Close()

	// Open a channel
	ch, err := conn.Channel()
	if err != nil {
		slog.Error("Failed to open RabbitMQ channel", "url", cfg.URL, "error", err, "component", "rabbitmq")
		details.WriteString(fmt.Sprintf("无法打开 RabbitMQ 通道: %v\n", err))
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "RabbitMQ告警", "服务异常", details.String(), hostIP, "alert", "rabbitmq", map[string]interface{}{"error": err.Error()})
	}
	defer ch.Close()

	// Check queue status using management API
	queues, err := listQueues(ctx, cfg)
	if err != nil {
		slog.Error("Failed to list RabbitMQ queues", "address", cfg.Address, "error", err, "component", "rabbitmq")
		details.WriteString(fmt.Sprintf("无法获取队列列表: %v\n", err))
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "RabbitMQ告警", "服务异常", details.String(), hostIP, "alert", "rabbitmq", map[string]interface{}{"error": err.Error()})
	}

	// Monitor queue metrics
	const maxMessages = 1000 // Threshold for messages in a queue
	const minConsumers = 1   // Minimum number of consumers
	for _, queue := range queues {
		if queue.Messages > maxMessages {
			hasIssue = true
			fmt.Fprintf(&details, "**队列 %s**:\n消息数: %d (超过阈值 %d)\n", alert.EscapeMarkdown(queue.Name), queue.Messages, maxMessages)
		}
		if queue.Consumers < minConsumers {
			hasIssue = true
			fmt.Fprintf(&details, "**队列 %s**:\n消费者数: %d (低于阈值 %d)\n", alert.EscapeMarkdown(queue.Name), queue.Consumers, minConsumers)
		}
	}

	// Check connection status
	connStatus := "正常✅"
	if conn.IsClosed() {
		connStatus = "异常❌ 连接已关闭"
		hasIssue = true
	}
	fmt.Fprintf(&details, "**连接状态**: %s\n", alert.EscapeMarkdown(connStatus))

	// Send alert if issues detected
	if hasIssue {
		slog.Info("RabbitMQ issues detected", "queues", len(queues), "connection_status", connStatus, "component", "rabbitmq")
		return util.SendAlert(ctx, bot, alertCache, cacheMutex, alertSilenceDuration, "RabbitMQ告警", "服务异常", details.String(), hostIP, "alert", "rabbitmq", map[string]interface{}{
			"queue_count": len(queues),
			"connection_status": connStatus,
		})
	}

	slog.Debug("No RabbitMQ issues detected", "queue_count", len(queues), "connection_status", connStatus, "component", "rabbitmq")
	return nil
}

// listQueues retrieves all RabbitMQ queues and their details using the management API.
func listQueues(ctx context.Context, cfg config.RabbitMQConfig) ([]QueueInfo, error) {
	// Validate and normalize management API address
	address := cfg.Address
	if !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "https://") {
		address = "http://" + address
	}
	queueURL := fmt.Sprintf("%s/api/queues", address)

	// Create HTTP client with optimized settings
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 5 * time.Second,
			DisableKeepAlives:   false,
		},
	}

	// Create request with basic auth
	req, err := http.NewRequestWithContext(ctx, "GET", queueURL, nil)
	if err != nil {
		slog.Error("Failed to create HTTP request for queue list", "url", queueURL, "error", err, "component", "rabbitmq")
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.SetBasicAuth(cfg.Username, cfg.Password)

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Failed to query RabbitMQ management API", "url", queueURL, "error", err, "component", "rabbitmq")
		return nil, fmt.Errorf("failed to query RabbitMQ management API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("Unexpected status code from RabbitMQ management API", "url", queueURL, "status_code", resp.StatusCode, "component", "rabbitmq")
		return nil, fmt.Errorf("unexpected status code from RabbitMQ management API: %d", resp.StatusCode)
	}

	// Parse response with limited reader
	const maxBodySize = 1 << 20 // 1MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		slog.Error("Failed to read RabbitMQ queue response", "url", queueURL, "error", err, "component", "rabbitmq")
		return nil, fmt.Errorf("failed to read queue response: %w", err)
	}

	var queues []QueueInfo
	if err := json.Unmarshal(body, &queues); err != nil {
		slog.Error("Failed to decode RabbitMQ queues", "url", queueURL, "error", err, "component", "rabbitmq")
		return nil, fmt.Errorf("failed to decode RabbitMQ queues: %w", err)
	}

	return queues, nil
}