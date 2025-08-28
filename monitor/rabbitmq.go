package monitor

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/rabbitmq/amqp091-go"
	"monitor-service/config"
)

// RabbitMQ checks the RabbitMQ connection.
func RabbitMQ(ctx context.Context, cfg config.RabbitMQConfig, clusterName string) (string, error) {
	conn, err := amqp091.Dial(cfg.URL)
	if err != nil {
		slog.Error("Failed to connect to RabbitMQ", "url", cfg.URL, "error", err)
		return fmt.Sprintf("**RabbitMQ (%s)**: Connection failed: %v", clusterName, err), err
	}
	defer conn.Close()
	return "", nil
}