package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// AlertBot handles sending alerts via Telegram and monitor-web.
type AlertBot struct {
	Bot           *tgbotapi.BotAPI
	ChatID        int64
	ClusterName   string
	Hostname      string
	ShowHostname  bool
	MonitorWebURL string // URL for monitor-web API endpoint
	RetryTimes    int    // Number of retry attempts
	RetryDelay    time.Duration
	fileMutex     sync.Mutex // Mutex for file operations
}

// AlertEvent represents the structure of alert events sent to monitor-web
type AlertEvent struct {
	Timestamp        time.Time `json:"timestamp"`
	Module           string    `json:"module"`
	ServiceName      string    `json:"service_name"`
	EventName        string    `json:"event_name"`
	Details          string    `json:"details"`
	HostIP           string    `json:"host_ip"`
	AlertType        string    `json:"alert_type"`
	ClusterName      string    `json:"cluster_name"`
	Hostname         string    `json:"hostname"`
	BigKeysCount     *int      `json:"big_keys_count,omitempty"`      // Redis-specific
	FailedNodes      *string   `json:"failed_nodes,omitempty"`        // Redis-specific
	DeadlocksInc     *int64    `json:"deadlocks_increment,omitempty"` // MySQL-specific
	SlowQueriesInc   *int64    `json:"slow_queries_increment,omitempty"`
	Connections      *int      `json:"connections,omitempty"`
	CPUUsage         *float64  `json:"cpu_usage,omitempty"`     // Host-specific
	MemRemaining     *float64  `json:"mem_remaining,omitempty"`
	DiskUsage        *float64  `json:"disk_usage,omitempty"`
	AddedUsers       *string   `json:"added_users,omitempty"`    // System-specific
	RemovedUsers     *string   `json:"removed_users,omitempty"`
	AddedProcesses   *string   `json:"added_processes,omitempty"`
	RemovedProcesses *string   `json:"removed_processes,omitempty"`
}

// NewAlertBot creates a new Telegram bot for alerts.
func NewAlertBot(botToken string, chatID int64, clusterName string, showHostname bool, monitorWebURL string, retryTimes int, retryDelay time.Duration) (*AlertBot, error) {
	// Allow empty botToken and chatID for modules without Telegram alerting
	if botToken == "" || chatID == 0 {
		slog.Debug("No Telegram bot configured", "bot_token_empty", botToken == "", "chat_id_zero", chatID == 0, "component", "alert")
		return &AlertBot{
			ClusterName:   clusterName,
			ShowHostname:  showHostname,
			MonitorWebURL: monitorWebURL,
			RetryTimes:    retryTimes,
			RetryDelay:    retryDelay,
		}, nil
	}
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		slog.Error("Failed to initialize Telegram bot", "error", err, "component", "alert")
		return nil, fmt.Errorf("failed to initialize Telegram bot: %w", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		slog.Warn("Failed to get hostname", "error", err, "component", "alert")
		hostname = "unknown"
	}
	slog.Info("Initialized alert bot", "cluster_name", clusterName, "show_hostname", showHostname, "monitor_web_url", monitorWebURL, "retry_times", retryTimes, "retry_delay", retryDelay, "component", "alert")
	return &AlertBot{
		Bot:           bot,
		ChatID:        chatID,
		ClusterName:   clusterName,
		Hostname:      hostname,
		ShowHostname:  showHostname,
		MonitorWebURL: monitorWebURL,
		RetryTimes:    retryTimes,
		RetryDelay:    retryDelay,
	}, nil
}

// FormatAlert creates a standardized Markdown alert message for Telegram.
func (a *AlertBot) FormatAlert(serviceName, eventName, details, hostIP, alertType string) string {
	if a == nil {
		return ""
	}
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	hostname := a.Hostname
	if !a.ShowHostname {
		hostname = "N/A"
	}
	var header string
	switch alertType {
	case "startup":
		header = "**🚀监控服务启动通知 Monitoring Service Startup🚀**"
	case "shutdown":
		header = "**🛑监控服务关闭通知 Monitoring Service Shutdown🛑**"
	default:
		header = "**🚨监控 Monitoring 告警 Alert🚨**"
	}

	// Escape all fields for MarkdownV2 to prevent parsing errors
	header = EscapeMarkdown(header)
	timestamp = EscapeMarkdown(timestamp)
	clusterName := EscapeMarkdown(a.ClusterName) // Correct: Use a.ClusterName
	hostname = EscapeMarkdown(hostname)
	hostIP = EscapeMarkdown(hostIP)
	serviceName = EscapeMarkdown(serviceName)
	eventName = EscapeMarkdown(eventName)
	details = EscapeMarkdown(details)

	// Build the alert message using strings.Builder for efficiency
	var msg strings.Builder
	fmt.Fprintf(&msg, "%s\n*时间*: %s\n*环境*: %s\n*主机名*: %s\n*主机IP*: %s\n*服务名*: %s\n*事件名*: %s\n*详情*:\n%s",
		header,
		timestamp,
		clusterName,
		hostname,
		hostIP,
		serviceName,
		eventName,
		details,
	)
	return msg.String()
}

// SendAlert sends a Telegram alert and pushes the alert to monitor-web with retry and persistence.
func (a *AlertBot) SendAlert(ctx context.Context, serviceName, eventName, details, hostIP, alertType string, module string, specificFields map[string]interface{}) error {
	if a == nil && a.MonitorWebURL == "" {
		slog.Warn("Alert bot and monitor-web URL are nil, skipping alert", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
		return nil
	}

	// Send Telegram alert if Bot is configured
	if a != nil && a.Bot != nil {
		message := a.FormatAlert(serviceName, eventName, details, hostIP, alertType)
		if message == "" {
			slog.Warn("Empty alert message, skipping Telegram alert", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
		} else {
			msg := tgbotapi.NewMessage(a.ChatID, message)
			msg.ParseMode = tgbotapi.ModeMarkdownV2

			// Send Telegram message with context
			ch := make(chan error, 1)
			go func() {
				_, err := a.Bot.Send(msg)
				ch <- err
			}()

			select {
			case <-ctx.Done():
				slog.Warn("Telegram alert sending cancelled", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
				return ctx.Err()
			case err := <-ch:
				if err != nil {
					slog.Error("Failed to send Telegram alert", "error", err, "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "alert_type", alertType, "component", "alert")
				} else {
					slog.Info("Sent Telegram alert", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "alert_type", alertType, "component", "alert")
				}
			}
		}
	}

	// Send alert to monitor-web if URL is configured
	if a.MonitorWebURL != "" {
		// Construct AlertEvent
		alertEvent := AlertEvent{
			Timestamp:   time.Now(),
			Module:      module,
			ServiceName: serviceName,
			EventName:   eventName,
			Details:     details,
			HostIP:      hostIP,
			AlertType:   alertType,
			ClusterName: a.ClusterName, // Correct: Use a.ClusterName
			Hostname:    a.Hostname,
		}

		// Populate module-specific fields
		if specificFields != nil {
			if bigKeysCount, ok := specificFields["big_keys_count"].(int); ok {
				alertEvent.BigKeysCount = &bigKeysCount
			}
			if failedNodes, ok := specificFields["failed_nodes"].(string); ok {
				alertEvent.FailedNodes = &failedNodes
			}
			if deadlocksInc, ok := specificFields["deadlocks_increment"].(int64); ok {
				alertEvent.DeadlocksInc = &deadlocksInc
			}
			if slowQueriesInc, ok := specificFields["slow_queries_increment"].(int64); ok {
				alertEvent.SlowQueriesInc = &slowQueriesInc
			}
			if connections, ok := specificFields["connections"].(int); ok {
				alertEvent.Connections = &connections
			}
			if cpuUsage, ok := specificFields["cpu_usage"].(float64); ok {
				alertEvent.CPUUsage = &cpuUsage
			}
			if memRemaining, ok := specificFields["mem_remaining"].(float64); ok {
				alertEvent.MemRemaining = &memRemaining
			}
			if diskUsage, ok := specificFields["disk_usage"].(float64); ok {
				alertEvent.DiskUsage = &diskUsage
			}
			if addedUsers, ok := specificFields["added_users"].(string); ok {
				alertEvent.AddedUsers = &addedUsers
			}
			if removedUsers, ok := specificFields["removed_users"].(string); ok {
				alertEvent.RemovedUsers = &removedUsers
			}
			if addedProcesses, ok := specificFields["added_processes"].(string); ok {
				alertEvent.AddedProcesses = &addedProcesses
			}
			if removedProcesses, ok := specificFields["removed_processes"].(string); ok {
				alertEvent.RemovedProcesses = &removedProcesses
			}
		}

		// Try sending with retries
		for attempt := 1; attempt <= a.RetryTimes; attempt++ {
			if err := a.sendToMonitorWeb(ctx, alertEvent); err == nil {
				slog.Info("Sent alert to monitor-web", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "alert_type", alertType, "attempt", attempt, "component", "alert")
				return nil
			} else {
				slog.Error("Failed to send alert to monitor-web", "error", err, "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "alert_type", alertType, "attempt", attempt, "component", "alert")
				if attempt < a.RetryTimes {
					select {
					case <-ctx.Done():
						slog.Warn("Alert sending cancelled during retry", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
						break
					case <-time.After(a.RetryDelay):
						continue
					}
				}
			}
		}

		// If all retries fail, persist to file
		if err := a.persistAlert(alertEvent); err != nil {
			slog.Error("Failed to persist alert to file", "error", err, "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
			return fmt.Errorf("failed to persist alert: %w", err)
		}
		slog.Info("Persisted alert to file after failed retries", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
	}

	return nil
}

// sendToMonitorWeb sends the alert to monitor-web.
func (a *AlertBot) sendToMonitorWeb(ctx context.Context, alertEvent AlertEvent) error {
	body, err := json.Marshal(alertEvent)
	if err != nil {
		return fmt.Errorf("failed to marshal alert event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.MonitorWebURL, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send alert to monitor-web: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("monitor-web returned status %d", resp.StatusCode)
	}

	return nil
}

// persistAlert appends the alert to unsent_alerts.json.
func (a *AlertBot) persistAlert(alertEvent AlertEvent) error {
	a.fileMutex.Lock()
	defer a.fileMutex.Unlock()

	filePath := "unsent_alerts.json"
	var alerts []AlertEvent

	// Read existing alerts
	data, err := os.ReadFile(filePath)
	if err == nil {
		if err := json.Unmarshal(data, &alerts); err != nil {
			return fmt.Errorf("failed to unmarshal existing alerts: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read unsent alerts file: %w", err)
	}

	// Append new alert
	alerts = append(alerts, alertEvent)

	// Write back to file
	data, err = json.MarshalIndent(alerts, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal alerts: %w", err)
	}

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open unsent alerts file: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("failed to write to unsent alerts file: %w", err)
	}

	return nil
}

// loadAndRetryUnsentAlerts loads unsent alerts from file and retries sending them.
func (a *AlertBot) loadAndRetryUnsentAlerts(ctx context.Context) error {
	if a.MonitorWebURL == "" {
		return nil
	}

	a.fileMutex.Lock()
	defer a.fileMutex.Unlock()

	filePath := "unsent_alerts.json"
	var alerts []AlertEvent

	// Read unsent alerts
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist, no unsent alerts
		}
		return fmt.Errorf("failed to read unsent alerts file: %w", err)
	}

	if err := json.Unmarshal(data, &alerts); err != nil {
		return fmt.Errorf("failed to unmarshal unsent alerts: %w", err)
	}

	if len(alerts) == 0 {
		return nil
	}

	// Try sending each alert
	var remaining []AlertEvent
	for _, alert := range alerts {
		for attempt := 1; attempt <= a.RetryTimes; attempt++ {
			if err := a.sendToMonitorWeb(ctx, alert); err == nil {
				slog.Info("Sent unsent alert to monitor-web", "service_name", alert.ServiceName, "event_name", alert.EventName, "host_ip", alert.HostIP, "alert_type", alert.AlertType, "attempt", attempt, "component", "alert")
				break
			} else {
				slog.Error("Failed to send unsent alert to monitor-web", "error", err, "service_name", alert.ServiceName, "event_name", alert.EventName, "host_ip", alert.HostIP, "alert_type", alert.AlertType, "attempt", attempt, "component", "alert")
				if attempt == a.RetryTimes {
					remaining = append(remaining, alert)
				} else {
					select {
					case <-ctx.Done():
						remaining = append(remaining, alert)
						break
					case <-time.After(a.RetryDelay):
						continue
					}
				}
			}
		}
	}

	// Write back remaining unsent alerts
	if len(remaining) == 0 {
		if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove unsent alerts file: %w", err)
		}
	} else {
		data, err := json.MarshalIndent(remaining, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal remaining unsent alerts: %w", err)
		}
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("failed to open unsent alerts file for writing: %w", err)
		}
		defer file.Close()
		if _, err := file.Write(data); err != nil {
			return fmt.Errorf("failed to write remaining unsent alerts: %w", err)
		}
	}

	return nil
}

// EscapeMarkdown escapes Telegram MarkdownV2 special characters to prevent formatting issues.
func EscapeMarkdown(text string) string {
	specialChars := []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}
	for _, char := range specialChars {
		text = strings.ReplaceAll(text, char, "\\"+char)
	}
	return text
}