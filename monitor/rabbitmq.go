package monitor

import (
	"context"
	"fmt"

	"github.com/rabbitmq/amqp091-go"
	"github.com/yourusername/monitor-service/config"
)

// RabbitMQ checks the RabbitMQ connection.
func RabbitMQ(ctx context.Context, cfg config.RabbitMQConfig) (string, error) {
	conn, err := amqp091.Dial(cfg.URL)
	if err != nil {
		return fmt.Sprintf("**RabbitMQ (%s)**: Connection failed: %v", cfg.ClusterName, err), err
	}
	defer conn.Close()
	return "", nil
}