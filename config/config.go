package config

import (
	"fmt"
	"github.com/spf13/viper"
)

// Config holds the application configuration.
type Config struct {
	Monitoring        MonitoringConfig        `mapstructure:"monitoring"`
	Telegram         TelegramConfig         `mapstructure:"telegram"`
	RabbitMQ         RabbitMQConfig         `mapstructure:"rabbitmq"`
	Redis            RedisConfig            `mapstructure:"redis"`
	MySQL            MySQLConfig            `mapstructure:"mysql"`
	Nacos            NacosConfig            `mapstructure:"nacos"`
	HostMonitoring   HostConfig             `mapstructure:"host_monitoring"`
	SystemMonitoring SystemConfig           `mapstructure:"system_monitoring"`
	AlertSilenceDuration string            `mapstructure:"alert_silence_duration"`
	CheckInterval        string            `mapstructure:"check_interval"`
}

// MonitoringConfig holds general monitoring settings.
type MonitoringConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

// TelegramConfig holds Telegram bot settings.
type TelegramConfig struct {
	BotToken string `mapstructure:"bot_token"`
	ChatID   int64  `mapstructure:"chat_id"`
}

// RabbitMQConfig holds RabbitMQ-specific configuration.
type RabbitMQConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	URL         string `mapstructure:"url"`
	ClusterName string `mapstructure:"cluster_name"`
}

// RedisConfig holds Redis-specific configuration.
type RedisConfig struct {
	Enabled         bool   `mapstructure:"enabled"`
	Addr            string `mapstructure:"addr"`
	Password        string `mapstructure:"password"`
	DB              int    `mapstructure:"db"`
	ClusterName     string `mapstructure:"cluster_name"`
	BigKeyThreshold int64  `mapstructure:"big_key_threshold"`
}

// MySQLConfig holds MySQL-specific configuration.
type MySQLConfig struct {
	Enabled        bool   `mapstructure:"enabled"`
	DSN            string `mapstructure:"dsn"`
	ClusterName    string `mapstructure:"cluster_name"`
	MaxConnections int    `mapstructure:"max_connections"`
}

// NacosConfig holds Nacos-specific configuration.
type NacosConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	Address     string `mapstructure:"address"`
	ClusterName string `mapstructure:"cluster_name"`
}

// HostConfig holds host monitoring configuration.
type HostConfig struct {
	Enabled          bool    `mapstructure:"enabled"`
	CPUThreshold     float64 `mapstructure:"cpu_threshold"`
	MemThreshold     float64 `mapstructure:"mem_threshold"`
	DiskThreshold    float64 `mapstructure:"disk_threshold"`
	NetIOThreshold   int64   `mapstructure:"net_io_threshold"`
	DiskIOThreshold  int64   `mapstructure:"disk_io_threshold"`
}

// SystemConfig holds system monitoring configuration.
type SystemConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

// LoadConfig loads the configuration from the specified YAML file.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	viper.SetConfigFile(path)
	viper.SetConfigType("yaml")
	if err := viper.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("failed to read config: %w", err)
	}
	if err := viper.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	return cfg, nil
}