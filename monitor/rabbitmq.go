package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/rabbitmq/amqp091-go"
	"monitor-service/alert"
	"monitor-service/config"
	"monitor-service/util"
)

// RabbitMQ monitors RabbitMQ instance and returns alerts if issues are detected.
func RabbitMQ(ctx context.Context, cfg config.RabbitMQConfig, bot *alert.AlertBot) ([]string, string, error) {
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
		msg := bot.FormatAlert(
			fmt.Sprintf("RabbitMQ (%s)", cfg.ClusterName),
			"服务异常",
			fmt.Sprintf("无法连接到 RabbitMQ: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer conn.Close()

	// Open a channel
	ch, err := conn.Channel()
	if err != nil {
		slog.Error("Failed to open RabbitMQ channel", "error", err, "component", "rabbitmq")
		msg := bot.FormatAlert(
			fmt.Sprintf("RabbitMQ (%s)", cfg.ClusterName),
			"服务异常",
			fmt.Sprintf("无法打开 RabbitMQ 通道: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to open RabbitMQ channel: %w", err)
	}
	defer ch.Close()

	// Check queue status (example: monitor all queues for high message counts)
	queues, err := listQueues(ch)
	if err != nil {
		slog.Error("Failed to list RabbitMQ queues", "error", err, "component", "rabbitmq")
		msg := bot.FormatAlert(
			fmt.Sprintf("RabbitMQ (%s)", cfg.ClusterName),
			"服务异常",
			fmt.Sprintf("无法获取队列列表: %v", err),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("failed to list RabbitMQ queues: %w", err)
	}

	// Monitor queue metrics
	const maxMessages = 1000 // Example threshold for messages in a queue
	for _, queue := range queues {
		if queue.Messages > maxMessages {
			hasIssue = true
			fmt.Fprintf(&details, "**队列 %s**:\n消息数: %d (超过阈值 %d)\n", queue.Name, queue.Messages, maxMessages)
		}
	}

	// Check connection and channel status
	connStatus := "正常✅"
	if conn.IsClosed() {
		connStatus = "异常❌ 连接已关闭"
		hasIssue = true
	}
	fmt.Fprintf(&details, "**连接状态**: %s\n", connStatus)

	if hasIssue {
		slog.Info("RabbitMQ issues detected", "queues", len(queues), "component", "rabbitmq")
		msg := bot.FormatAlert(
			fmt.Sprintf("RabbitMQ (%s)", cfg.ClusterName),
			"服务异常",
			details.String(),
			hostIP,
			"alert",
		)
		return []string{msg}, hostIP, fmt.Errorf("RabbitMQ issues detected")
	}

	slog.Debug("No RabbitMQ issues detected", "component", "rabbitmq")
	return nil, hostIP, nil
}

// listQueues retrieves all RabbitMQ queues and their details.
func listQueues(ch *amqp091.Channel) ([]amqp091.Queue, error) {
	var queues []amqp091.Queue
	// RabbitMQ does not provide a direct API to list queues, so we assume a list of known queues.
	exampleQueues := []string{"queue1", "queue2", "queue3"} // Replace with actual queue names or fetch dynamically
	for _, queueName := range exampleQueues {
		select {
		case <-time.After(time.Second):
			// Proceed with queue inspection
		case <-ch.NotifyClose(make(chan *amqp091.Error)):
			return nil, fmt.Errorf("channel closed unexpectedly")
		}
		queue, err := ch.QueueInspect(queueName)
		if err != nil {
			slog.Warn("Failed to inspect queue", "queue", queueName, "error", err, "component", "rabbitmq")
			continue
		}
		queues = append(queues, queue)
	}
	return queues, nil
}