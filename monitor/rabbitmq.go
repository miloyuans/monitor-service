package monitor

import (
	"context"
	"fmt"
	"github.com/rabbitmq/amqp091-go"
)

// RabbitMQConfig holds RabbitMQ-specific configuration.
type RabbitMQConfig struct {
	Enabled     bool
	URL         string
	ClusterName string
}

// RabbitMQ checks the RabbitMQ connection.
func RabbitMQ(ctx context.Context, cfg RabbitMQConfig) (string, error) {
	conn, err := amqp091.Dial(cfg.URL)
	if err != nil {
		return fmt.Sprintf("**RabbitMQ (%s)**: Connection failed: %v", cfg.ClusterName, err), err
	}
	defer conn.Close()
	return "", nil
}