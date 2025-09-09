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
	MonitorWebURL string
	RetryTimes    int
	RetryDelay    time.Duration
	fileMutex     sync.Mutex
}

// AlertEvent represents the structure of alert events sent to monitor-web.
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
	BigKeysCount     *int      `json:"big_keys_count,omitempty"`
	FailedNodes      *string   `json:"failed_nodes,omitempty"`
	DeadlocksInc     *int64    `json:"deadlocks_increment,omitempty"`
	SlowQueriesInc   *int64    `json:"slow_queries_increment,omitempty"`
	Connections      *int      `json:"connections,omitempty"`
	CPUUsage         *float64  `json:"cpu_usage,omitempty"`
	MemRemaining     *float64  `json:"mem_remaining,omitempty"`
	DiskUsage        *float64  `json:"disk_usage,omitempty"`
	AddedUsers       *string   `json:"added_users,omitempty"`
	RemovedUsers     *string   `json:"removed_users,omitempty"`
	AddedProcesses   *string   `json:"added_processes,omitempty"`
	RemovedProcesses *string   `json:"removed_processes,omitempty"`
}

// NewAlertBot creates a new Telegram bot for alerts.
func NewAlertBot(botToken string, chatID int64, clusterName string, showHostname bool, monitorWebURL string, retryTimes int, retryDelay time.Duration) (*AlertBot, error) {
	if botToken == "" && monitorWebURL == "" {
		return nil, fmt.Errorf("either bot_token or monitor_web_url must be provided")
	}
	var bot *tgbotapi.BotAPI
	if botToken != "" {
		var err error
		bot, err = tgbotapi.NewBotAPI(botToken)
		if err != nil {
			slog.Error("Failed to initialize Telegram bot", "error", err, "component", "alert")
			return nil, fmt.Errorf("failed to initialize Telegram bot: %w", err)
		}
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

	header = EscapeMarkdown(header)
	timestamp = EscapeMarkdown(timestamp)
	clusterName := EscapeMarkdown(a.ClusterName)
	hostname = EscapeMarkdown(hostname)
	hostIP = EscapeMarkdown(hostIP)
	serviceName = EscapeMarkdown(serviceName)
	eventName = EscapeMarkdown(eventName)
	details = EscapeMarkdown(details)

	var msg strings.Builder
	fmt.Fprintf(&msg, "%s\n*时间*: %s\n*环境*: %s\n*主机名*: %s\n*主机IP*: %s\n*服务名*: %s\n*事件名*: %s\n*详情*:\n%s",
		header, timestamp, clusterName, hostname, hostIP, serviceName, eventName, details)
	return msg.String()
}

// SendAlert sends a Telegram alert and pushes to monitor-web with retry and persistence.
func (a *AlertBot) SendAlert(ctx context.Context, serviceName, eventName, details, hostIP, alertType, module string, specificFields map[string]interface{}) error {
	if a == nil && a.MonitorWebURL == "" {
		slog.Warn("Alert bot and monitor-web URL are nil, skipping alert", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
		return nil
	}

	var errors []error
	if a != nil && a.Bot != nil {
		message := a.FormatAlert(serviceName, eventName, details, hostIP, alertType)
		if message == "" {
			slog.Warn("Empty alert message, skipping Telegram alert", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
		} else {
			msg := tgbotapi.NewMessage(a.ChatID, message)
			msg.ParseMode = tgbotapi.ModeMarkdownV2

			taskCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			ch := make(chan error, 1)
			go func() {
				_, err := a.Bot.Send(msg)
				ch <- err
			}()

			select {
			case <-taskCtx.Done():
				slog.Warn("Telegram alert sending cancelled", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
				errors = append(errors, taskCtx.Err())
			case err := <-ch:
				if err != nil {
					slog.Error("Failed to send Telegram alert", "error", err, "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "alert_type", alertType, "component", "alert")
					errors = append(errors, err)
				} else {
					slog.Info("Sent Telegram alert", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "alert_type", alertType, "component", "alert")
				}
			}
		}
	}

	if a.MonitorWebURL != "" {
		alertEvent := AlertEvent{
			Timestamp:   time.Now(),
			Module:      module,
			ServiceName: serviceName,
			EventName:   eventName,
			Details:     details,
			HostIP:      hostIP,
			AlertType:   alertType,
			ClusterName: a.ClusterName,
			Hostname:    a.Hostname,
		}

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

		for attempt := 1; attempt <= a.RetryTimes; attempt++ {
			taskCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			if err := a.sendToMonitorWeb(taskCtx, alertEvent); err == nil {
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
				errors = append(errors, err)
			}
		}

		if err := a.persistAlert(alertEvent); err != nil {
			slog.Error("Failed to persist alert to file", "error", err, "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
			errors = append(errors, fmt.Errorf("failed to persist alert: %w", err))
		} else {
			slog.Info("Persisted alert to file after failed retries", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to send alert: %v", errors)
	}
	return nil
}

// sendToMonitorWeb sends the alert to monitor-web with timeout.
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

// persistAlert appends the alert to unsent_alerts.json with reduced lock contention.
func (a *AlertBot) persistAlert(alertEvent AlertEvent) error {
	a.fileMutex.Lock()
	defer a.fileMutex.Unlock()

	filePath := "unsent_alerts.json"
	var alerts []AlertEvent

	data, err := os.ReadFile(filePath)
	if err == nil {
		if err := json.Unmarshal(data, &alerts); err != nil {
			return fmt.Errorf("failed to unmarshal existing alerts: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read unsent alerts file: %w", err)
	}

	alerts = append(alerts, alertEvent)

	data, err = json.MarshalIndent(alerts, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal alerts: %w", err)
	}

	// Write to temporary file to avoid corruption
	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write to temporary file: %w", err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		return fmt.Errorf("failed to rename temporary file: %w", err)
	}

	return nil
}

// LoadAndRetryUnsentAlerts loads unsent alerts and retries sending them with concurrency control.
func (a *AlertBot) LoadAndRetryUnsentAlerts(ctx context.Context) error {
	if a.MonitorWebURL == "" {
		slog.Debug("No monitor-web URL configured, skipping unsent alerts retry", "component", "alert")
		return nil
	}

	a.fileMutex.Lock()
	filePath := "unsent_alerts.json"
	data, err := os.ReadFile(filePath)
	if err != nil {
		a.fileMutex.Unlock()
		if os.IsNotExist(err) {
			slog.Debug("No unsent alerts file found", "file", filePath, "component", "alert")
			return nil
		}
		return fmt.Errorf("failed to read unsent alerts file: %w", err)
	}

	var alerts []AlertEvent
	if err := json.Unmarshal(data, &alerts); err != nil {
		a.fileMutex.Unlock()
		return fmt.Errorf("failed to unmarshal unsent alerts: %w", err)
	}
	a.fileMutex.Unlock()

	if len(alerts) == 0 {
		slog.Debug("No unsent alerts to retry", "file", filePath, "component", "alert")
		return nil
	}

	// Use a worker pool to limit concurrency
	const maxWorkers = 5
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errors []error
	var remaining []AlertEvent

	for _, alert := range alerts {
		select {
		case <-ctx.Done():
			mu.Lock()
			remaining = append(remaining, alert)
			mu.Unlock()
			continue
		case sem <- struct{}{}:
			wg.Add(1)
			go func(alert AlertEvent) {
				defer wg.Done()
				defer func() { <-sem }()

				for attempt := 1; attempt <= a.RetryTimes; attempt++ {
					taskCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					defer cancel()

					if err := a.sendToMonitorWeb(taskCtx, alert); err == nil {
						slog.Info("Sent unsent alert to monitor-web", "service_name", alert.ServiceName, "event_name", alert.EventName, "host_ip", alert.HostIP, "alert_type", alert.AlertType, "attempt", attempt, "component", "alert")
						return
					}
					slog.Error("Failed to send unsent alert to monitor-web", "error", err, "service_name", alert.ServiceName, "event_name", alert.EventName, "host_ip", alert.HostIP, "alert_type", alert.AlertType, "attempt", attempt, "component", "alert")
					if attempt < a.RetryTimes {
						select {
						case <-ctx.Done():
							mu.Lock()
							remaining = append(remaining, alert)
							mu.Unlock()
							return
						case <-time.After(a.RetryDelay):
							continue
						}
					}
					mu.Lock()
					remaining = append(remaining, alert)
					mu.Unlock()
				}
				mu.Lock()
				errors = append(errors, fmt.Errorf("failed to retry alert for %s: all %d attempts failed", alert.ServiceName, a.RetryTimes))
				mu.Unlock()
			}(alert)
		}
	}
	wg.Wait()

	// Write back remaining unsent alerts
	if len(remaining) == 0 {
		a.fileMutex.Lock()
		if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
			a.fileMutex.Unlock()
			return fmt.Errorf("failed to remove unsent alerts file: %w", err)
		}
		a.fileMutex.Unlock()
		slog.Info("Cleared unsent alerts file", "file", filePath, "component", "alert")
	} else {
		data, err := json.MarshalIndent(remaining, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal remaining unsent alerts: %w", err)
		}
		a.fileMutex.Lock()
		if err := os.WriteFile(filePath, data, 0644); err != nil {
			a.fileMutex.Unlock()
			return fmt.Errorf("failed to write remaining unsent alerts: %w", err)
		}
		a.fileMutex.Unlock()
		slog.Info("Saved remaining unsent alerts", "file", filePath, "count", len(remaining), "component", "alert")
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to retry some alerts: %v", errors)
	}
	return nil
}

// EscapeMarkdown escapes Telegram MarkdownV2 special characters.
func EscapeMarkdown(text string) string {
	specialChars := []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}
	for _, char := range specialChars {
		text = strings.ReplaceAll(text, char, "\\"+char)
	}
	return text
}