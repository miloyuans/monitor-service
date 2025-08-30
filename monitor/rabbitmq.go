package monitor

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/rabbitmq/amqp091-go"
	"monitor-service/alert"
	"monitor-service/config"
)

// RabbitMQ checks the RabbitMQ connection, queue status, and connection count.
func RabbitMQ(ctx context.Context, cfg config.RabbitMQConfig, bot *alert.AlertBot) ([]string, string, error) {
	var hostIP string // Placeholder for host IP, to be implemented if needed
	var messages []string

	// Initialize RabbitMQ connection
	conn, err := amqp091.DialConfig(cfg.URL, amqp091.Config{
		Dial:      amqp091.DefaultDial(5 * time.Second),
		Heartbeat: 10 * time.Second,
	})
	if err != nil {
		slog.Error("Failed to connect to RabbitMQ", "url", cfg.URL, "error", err, "component", "rabbitmq")
		msg := bot.FormatAlert(
			fmt.Sprintf("RabbitMQ (%s)", cfg.ClusterName),
			"连接失败",
			fmt.Sprintf("无法连接到 RabbitMQ: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer conn.Close()

	// Open a channel to perform additional checks
	ch, err := conn.Channel()
	if err != nil {
		slog.Error("Failed to open RabbitMQ channel", "error", err, "component", "rabbitmq")
		msg := bot.FormatAlert(
			fmt.Sprintf("RabbitMQ (%s)", cfg.ClusterName),
			"通道打开失败",
			fmt.Sprintf("无法打开 RabbitMQ 通道: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to open RabbitMQ channel: %w", err)
	}
	defer ch.Close()

	// Check queue status (example: check for high message count)
	// Note: Requires management plugin or API for detailed checks; this is a basic example
	queueName := "monitor-service-queue" // Replace with actual queue name or make configurable
	queue, err := ch.QueueInspect(ctx, queueName)
	if err != nil {
		slog.Warn("Failed to inspect RabbitMQ queue", "queue", queueName, "error", err, "component", "rabbitmq")
		msg := bot.FormatAlert(
			fmt.Sprintf("RabbitMQ (%s)", cfg.ClusterName),
			"队列检查失败",
			fmt.Sprintf("无法检查队列 %s: %v", queueName, err),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	} else if queue.Messages > 1000 { // Arbitrary threshold, make configurable if needed
		msg := bot.FormatAlert(
			fmt.Sprintf("RabbitMQ (%s)", cfg.ClusterName),
			"队列消息堆积",
			fmt.Sprintf("队列 %s 消息数过多: %d", queueName, queue.Messages),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	}

	// Check connection count (requires management API for accurate data)
	// This is a placeholder; actual implementation may require HTTP client to RabbitMQ management API
	connCount, err := getConnectionCount(ctx, cfg.URL)
	if err != nil {
		slog.Warn("Failed to check RabbitMQ connection count", "error", err, "component", "rabbitmq")
		msg := bot.FormatAlert(
			fmt.Sprintf("RabbitMQ (%s)", cfg.ClusterName),
			"连接数检查失败",
			fmt.Sprintf("无法检查连接数: %v", err),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	} else if connCount > 100 { // Arbitrary threshold, make configurable if needed
		msg := bot.FormatAlert(
			fmt.Sprintf("RabbitMQ (%s)", cfg.ClusterName),
			"连接数过高",
			fmt.Sprintf("当前连接数 %d 超过阈值 100", connCount),
			hostIP,
			"alert",
		)
		messages = append(messages, msg)
	}

	if len(messages) > 0 {
		return messages, hostIP, fmt.Errorf("RabbitMQ issues detected")
	}
	return nil, hostIP, nil
}

// getConnectionCount is a placeholder for retrieving connection count (e.g., via RabbitMQ management API).
func getConnectionCount(ctx context.Context, url string) (int, error) {
	// Placeholder: Implement actual logic using RabbitMQ management API (e.g., HTTP client)
	// Example: Query http://<host>:15672/api/connections with authentication
	slog.Warn("Connection count check not implemented", "component", "rabbitmq")
	return 0, fmt.Errorf("connection count check not implemented")
}