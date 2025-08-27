package config

import (
	"fmt"
	"github.com/spf13/viper"
)

// Config holds the application configuration.
type Config struct {
	Monitoring struct {
		Enabled bool `mapstructure:"enabled"`
	} `mapstructure:"monitoring"`
	Telegram struct {
		BotToken string `mapstructure:"bot_token"`
		ChatID   int64  `mapstructure:"chat_id"`
	} `mapstructure:"telegram"`
	RabbitMQ struct {
		Enabled     bool   `mapstructure:"enabled"`
		URL         string `mapstructure:"url"`
		ClusterName string `mapstructure:"cluster_name"`
	} `mapstructure:"rabbitmq"`
	Redis struct {
		Enabled         bool   `mapstructure:"enabled"`
		Addr            string `mapstructure:"addr"`
		Password        string `mapstructure:"password"`
		DB              int    `mapstructure:"db"`
		ClusterName     string `mapstructure:"cluster_name"`
		BigKeyThreshold int64  `mapstructure:"big_key_threshold"`
	} `mapstructure:"redis"`
	MySQL struct {
		Enabled        bool   `mapstructure:"enabled"`
		DSN            string `mapstructure:"dsn"`
		ClusterName    string `mapstructure:"cluster_name"`
		MaxConnections int    `mapstructure:"max_connections"`
	} `mapstructure:"mysql"`
	Elasticsearch struct {
		Enabled     bool     `mapstructure:"enabled"`
		Addresses   []string `mapstructure:"addresses"`
		ClusterName string   `mapstructure:"cluster_name"`
	} `mapstructure:"elasticsearch"`
	Nacos struct {
		Enabled     bool   `mapstructure:"enabled"`
		Address     string `mapstructure:"address"`
		ClusterName string `mapstructure:"cluster_name"`
	} `mapstructure:"nacos"`
	HostMonitoring struct {
		Enabled          bool    `mapstructure:"enabled"`
		CPUThreshold     float64 `mapstructure:"cpu_threshold"`
		MemThreshold     float64 `mapstructure:"mem_threshold"`
		DiskThreshold    float64 `mapstructure:"disk_threshold"`
		NetIOThreshold   int64   `mapstructure:"net_io_threshold"`
		DiskIOThreshold  int64   `mapstructure:"disk_io_threshold"`
	} `mapstructure:"host_monitoring"`
	SystemMonitoring struct {
		Enabled bool `mapstructure:"enabled"`
	} `mapstructure:"system_monitoring"`
	AlertSilenceDuration string `mapstructure:"alert_silence_duration"`
	CheckInterval        string `mapstructure:"check_interval"`
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