package config

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/viper"
)

// Config holds the application configuration.
type Config struct {
	Monitoring           GeneralConfig    `mapstructure:"monitoring"`
	Telegram             TelegramConfig   `mapstructure:"telegram"`
	RabbitMQ             RabbitMQConfig   `mapstructure:"rabbitmq"`
	Redis                RedisConfig      `mapstructure:"redis"`
	MySQL                MySQLConfig      `mapstructure:"mysql"`
	Nacos                NacosConfig      `mapstructure:"nacos"`
	HostMonitoring       HostConfig       `mapstructure:"host_monitoring"`
	SystemMonitoring     SystemConfig     `mapstructure:"system_monitoring"`
	ClusterName          string           `mapstructure:"cluster_name"`
	ShowHostname         bool             `mapstructure:"show_hostname"`
	AlertSilenceDuration int              `mapstructure:"alert_silence_duration"`
	CheckInterval        string           `mapstructure:"check_interval"`
}

// GeneralConfig holds general monitoring settings.
type GeneralConfig struct {
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
	Username    string `mapstructure:"username"`
	Password    string `mapstructure:"password"`
	Address     string `mapstructure:"address"` // Management API URL
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
	Enabled             	bool          `mapstructure:"enabled"`
	DSN                 	string        `mapstructure:"dsn"`
	ClusterName         	string        `mapstructure:"cluster_name"`
	MaxConnections      	int           `mapstructure:"max_connections"`
	DeadlockThreshold   	int64         `mapstructure:"deadlock_threshold"`
	SlowQueryThreshold  	int64         `mapstructure:"slow_query_threshold"`
	SecondsBehindThreshold	int64		  `mapstructure:"seconds_behind_threshold"`
	Telegram            	TelegramConfig `mapstructure:"telegram"`
}

// NacosConfig holds Nacos-specific configuration.
type NacosConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	Address     string `mapstructure:"address"`
	ClusterName string `mapstructure:"cluster_name"`
	NacosDataID string `mapstructure:"nacos_data_id"`
	NacosGroup  string `mapstructure:"nacos_group"`
}

// HostConfig holds host monitoring configuration.
type HostConfig struct {
	Enabled         bool    `mapstructure:"enabled"`
	CPUThreshold    float64 `mapstructure:"cpu_threshold"`
	MemThreshold    float64 `mapstructure:"mem_threshold"`
	DiskThreshold   float64 `mapstructure:"disk_threshold"`
	NetIOThreshold  float64 `mapstructure:"net_io_threshold"`  // GB/s
	DiskIOThreshold float64 `mapstructure:"disk_io_threshold"` // GB/s
}

// SystemConfig holds system monitoring configuration.
type SystemConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

// LoadConfig loads the configuration from the specified YAML file.
func LoadConfig(path string) (Config, error) {
	// Set default values
	viper.SetDefault("monitoring.enabled", false)
	viper.SetDefault("telegram.bot_token", "")
	viper.SetDefault("telegram.chat_id", 0)
	viper.SetDefault("rabbitmq.enabled", false)
	viper.SetDefault("rabbitmq.url", "amqp://guest:guest@localhost:5672/")
	viper.SetDefault("rabbitmq.username", "guest")
	viper.SetDefault("rabbitmq.password", "guest")
	viper.SetDefault("rabbitmq.address", "http://localhost:15672")
	viper.SetDefault("rabbitmq.cluster_name", "rabbitmq-cluster")
	viper.SetDefault("redis.enabled", false)
	viper.SetDefault("redis.addr", "localhost:6379")
	viper.SetDefault("redis.password", "")
	viper.SetDefault("redis.db", 0)
	viper.SetDefault("redis.cluster_name", "redis-cluster")
	viper.SetDefault("redis.big_key_threshold", 1048576) // 1MB
	viper.SetDefault("mysql.enabled", false)
	viper.SetDefault("mysql.dsn", "root:password@tcp(localhost:3306)/mysql")
	viper.SetDefault("mysql.cluster_name", "mysql-cluster")
	viper.SetDefault("mysql.max_connections", 100)
	viper.SetDefault("mysql.telegram.bot_token", "")
	viper.SetDefault("mysql.telegram.chat_id", 0)
	viper.SetDefault("mysql.deadlock_threshold", 1)
	viper.SetDefault("mysql.slow_query_threshold", 1)
	viper.SetDefault("mysql.seconds_behind_threshold", 1)
	viper.SetDefault("nacos.enabled", false)
	viper.SetDefault("nacos.address", "http://localhost:8848")
	viper.SetDefault("nacos.cluster_name", "nacos-cluster")
	viper.SetDefault("nacos.nacos_data_id", "example")
	viper.SetDefault("nacos.nacos_group", "DEFAULT_GROUP")
	viper.SetDefault("host_monitoring.enabled", false)
	viper.SetDefault("host_monitoring.cpu_threshold", 80.0)
	viper.SetDefault("host_monitoring.mem_threshold", 80.0)
	viper.SetDefault("host_monitoring.disk_threshold", 90.0)
	viper.SetDefault("host_monitoring.net_io_threshold", 1.0)   // 1 GB/s
	viper.SetDefault("host_monitoring.disk_io_threshold", 1.0) // 1 GB/s
	viper.SetDefault("system_monitoring.enabled", false)
	viper.SetDefault("cluster_name", "default-cluster")
	viper.SetDefault("show_hostname", false)
	viper.SetDefault("alert_silence_duration", 5) // minutes
	viper.SetDefault("check_interval", "30s")

	var cfg Config
	viper.SetConfigFile(path)
	viper.SetConfigType("yaml")
	if err := viper.ReadInConfig(); err != nil {
		slog.Error("Failed to read config file", "error", err, "path", path, "component", "config")
		return cfg, fmt.Errorf("failed to read config file %s: %w", path, err)
	}
	if err := viper.Unmarshal(&cfg); err != nil {
		slog.Error("Failed to unmarshal config", "error", err, "component", "config")
		return cfg, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		slog.Error("Config validation failed", "error", err, "component", "config")
		return cfg, fmt.Errorf("config validation failed: %w", err)
	}

	// Log idle state if all monitoring features are disabled
	if !cfg.IsAnyMonitoringEnabled() {
		slog.Info("All monitoring features are disabled", "message", "System is idle; no monitoring tasks will be executed", "component", "config")
	}

	slog.Info("Loaded configuration", "path", path, "component", "config")
	return cfg, nil
}

// Validate validates the configuration parameters.
func (c Config) Validate() error {
	// Validate global Telegram configuration (optional if no monitoring is enabled or module-specific config exists)
	if c.IsAnyMonitoringEnabled() && c.Telegram.BotToken == "" && !c.MySQL.HasIndependentTelegramConfig() {
		return fmt.Errorf("telegram.bot_token is required when any monitoring is enabled and no module-specific Telegram config is provided")
	}
	if c.IsAnyMonitoringEnabled() && c.Telegram.ChatID == 0 && !c.MySQL.HasIndependentTelegramConfig() {
		return fmt.Errorf("telegram.chat_id is required and must be non-zero when any monitoring is enabled and no module-specific Telegram config is provided")
	}

	// Validate cluster name
	if c.ClusterName == "" {
		return fmt.Errorf("cluster_name is required")
	}

	// Validate check interval
	if c.CheckInterval == "" {
		return fmt.Errorf("check_interval is required")
	}
	if _, err := time.ParseDuration(c.CheckInterval); err != nil {
		return fmt.Errorf("invalid check_interval format: %w", err)
	}

	// Validate alert silence duration
	if c.AlertSilenceDuration <= 0 {
		return fmt.Errorf("alert_silence_duration must be positive")
	}

	// Validate RabbitMQ configuration
	if c.RabbitMQ.Enabled {
		if c.RabbitMQ.URL == "" {
			return fmt.Errorf("rabbitmq.url is required when rabbitmq.enabled is true")
		}
		if c.RabbitMQ.Username == "" {
			return fmt.Errorf("rabbitmq.username is required when rabbitmq.enabled is true")
		}
		if c.RabbitMQ.Password == "" {
			return fmt.Errorf("rabbitmq.password is required when rabbitmq.enabled is true")
		}
		if c.RabbitMQ.Address == "" {
			return fmt.Errorf("rabbitmq.address is required when rabbitmq.enabled is true")
		}
		if c.RabbitMQ.ClusterName == "" {
			return fmt.Errorf("rabbitmq.cluster_name is required when rabbitmq.enabled is true")
		}
	}

	// Validate Redis configuration
	if c.Redis.Enabled {
		if c.Redis.Addr == "" {
			return fmt.Errorf("redis.addr is required when redis.enabled is true")
		}
		if c.Redis.ClusterName == "" {
			return fmt.Errorf("redis.cluster_name is required when redis.enabled is true")
		}
		if c.Redis.BigKeyThreshold <= 0 {
			return fmt.Errorf("redis.big_key_threshold must be positive")
		}
		if c.Redis.DB < 0 {
			return fmt.Errorf("redis.db must be non-negative")
		}
	}

	// Validate MySQL configuration
	if c.MySQL.Enabled {
		if c.MySQL.DSN == "" {
			return fmt.Errorf("mysql.dsn is required when mysql.enabled is true")
		}
		if c.MySQL.ClusterName == "" {
			return fmt.Errorf("mysql.cluster_name is required when mysql.enabled is true")
		}
		if c.MySQL.MaxConnections <= 0 {
			return fmt.Errorf("mysql.max_connections must be positive")
		}
		// ... existing
		if c.MySQL.DeadlockThreshold <= 0 {
			return fmt.Errorf("mysql.deadlock_threshold must be non-negative")
		}
		if c.MySQL.SlowQueryThreshold <= 0 {
			return fmt.Errorf("mysql.slow_query_threshold must be non-negative")
		}
		if c.MySQL.SecondsBehindThreshold <= 0 {
			return fmt.Errorf("mysql.seconds_behind_threshold must be non-negative")
		}
		// Validate MySQL Telegram configuration if provided
		if c.MySQL.HasIndependentTelegramConfig() {
			if c.MySQL.Telegram.BotToken == "" {
				return fmt.Errorf("mysql.telegram.bot_token is required when mysql.telegram is configured")
			}
			if c.MySQL.Telegram.ChatID == 0 {
				return fmt.Errorf("mysql.telegram.chat_id is required and must be non-zero when mysql.telegram is configured")
			}
		}
	}

	// Validate Nacos configuration
	if c.Nacos.Enabled {
		if c.Nacos.Address == "" {
			return fmt.Errorf("nacos.address is required when nacos.enabled is true")
		}
		if c.Nacos.ClusterName == "" {
			return fmt.Errorf("nacos.cluster_name is required when nacos.enabled is true")
		}
		if c.Nacos.NacosDataID == "" {
			return fmt.Errorf("nacos.nacos_data_id is required when nacos.enabled is true")
		}
		if c.Nacos.NacosGroup == "" {
			return fmt.Errorf("nacos.nacos_group is required when nacos.enabled is true")
		}
	}

	// Validate Host monitoring configuration
	if c.HostMonitoring.Enabled {
		if c.HostMonitoring.CPUThreshold <= 0 || c.HostMonitoring.CPUThreshold > 100 {
			return fmt.Errorf("host_monitoring.cpu_threshold must be between 0 and 100")
		}
		if c.HostMonitoring.MemThreshold <= 0 || c.HostMonitoring.MemThreshold > 100 {
			return fmt.Errorf("host_monitoring.mem_threshold must be between 0 and 100")
		}
		if c.HostMonitoring.DiskThreshold <= 0 || c.HostMonitoring.DiskThreshold > 100 {
			return fmt.Errorf("host_monitoring.disk_threshold must be between 0 and 100")
		}
		if c.HostMonitoring.NetIOThreshold <= 0 {
			return fmt.Errorf("host_monitoring.net_io_threshold must be positive")
		}
		if c.HostMonitoring.DiskIOThreshold <= 0 {
			return fmt.Errorf("host_monitoring.disk_io_threshold must be positive")
		}
	}

	// System monitoring validation
	if c.SystemMonitoring.Enabled {
		// No specific validation required yet
	}

	return nil
}

// HasIndependentTelegramConfig checks if MySQL has its own Telegram configuration.
func (m MySQLConfig) HasIndependentTelegramConfig() bool {
	return m.Telegram.BotToken != "" && m.Telegram.ChatID != 0
}

// IsAnyMonitoringEnabled checks if any monitoring feature is enabled.
func (c Config) IsAnyMonitoringEnabled() bool {
	return c.Monitoring.Enabled ||
		c.RabbitMQ.Enabled ||
		c.Redis.Enabled ||
		c.MySQL.Enabled ||
		c.Nacos.Enabled ||
		c.HostMonitoring.Enabled ||
		c.SystemMonitoring.Enabled
}