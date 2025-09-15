package alert

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// UnsentAlert stores alerts that failed to send, tracking pending destinations.
type UnsentAlert struct {
	ID                  string    `json:"id"` // Unique alert ID
	Title               string    `json:"title"`
	Event               string    `json:"event"`
	Details             string    `json:"details"`
	HostIP              string    `json:"host_ip"`
	AlertType           string    `json:"alert_type"`
	Module              string    `json:"module"`
	PendingDestinations []string  `json:"pending_destinations"`
	Timestamp           time.Time `json:"timestamp"`
	SpecificFields      map[string]interface{} `json:"specific_fields"`
}

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

// SendAlert sends an alert to specified destinations, tracking success/failure.
func (a *AlertBot) SendAlert(ctx context.Context, serviceName, eventName, details, hostIP, alertType, module string, specificFields map[string]interface{}) error {
	if a == nil && a.MonitorWebURL == "" {
		slog.Warn("Alert bot and monitor-web URL are nil, skipping alert", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
		return nil
	}

	// Determine destinations
	destinations := []string{"telegram"}
	if a.MonitorWebURL != "" {
		destinations = append(destinations, "web")
	}
	if dests, ok := specificFields["destinations"].([]string); ok && len(dests) > 0 {
		destinations = dests
	}

	// Generate unique alert ID
	alertID := specificFields["alert_key"].(string)
	if alertID == "" {
		hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:%s:%s", serviceName, eventName, details, hostIP, time.Now().String())))
		alertID = hex.EncodeToString(hash[:])
	}

	unsent := &UnsentAlert{
		ID:                  alertID,
		Title:              serviceName,
		Event:              eventName,
		Details:            details,
		HostIP:             hostIP,
		AlertType:          alertType,
		Module:             module,
		PendingDestinations: destinations,
		Timestamp:          time.Now(),
		SpecificFields:     specificFields,
	}

	var errors []error
	for i := len(unsent.PendingDestinations) - 1; i >= 0; i-- {
		dest := unsent.PendingDestinations[i]
		for attempt := 1; attempt <= a.RetryTimes; attempt++ {
			taskCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			err := a.sendToDestination(taskCtx, dest, unsent)
			if err == nil {
				slog.Info("Sent alert to destination", "alert_id", alertID, "destination", dest, "service_name", serviceName, "event_name", eventName, "attempt", attempt, "component", "alert")
				unsent.PendingDestinations = append(unsent.PendingDestinations[:i], unsent.PendingDestinations[i+1:]...)
				break
			}
			slog.Error("Failed to send alert to destination", "alert_id", alertID, "destination", dest, "error", err, "service_name", serviceName, "event_name", eventName, "attempt", attempt, "component", "alert")
			if attempt < a.RetryTimes {
				select {
				case <-ctx.Done():
					errors = append(errors, ctx.Err())
					break
				case <-time.After(a.RetryDelay):
					continue
				}
			}
			errors = append(errors, err)
		}
	}

	// Persist if there are pending destinations
	if len(unsent.PendingDestinations) > 0 {
		if err := a.persistAlert(*unsent); err != nil {
			slog.Error("Failed to persist alert", "alert_id", alertID, "error", err, "component", "alert")
			errors = append(errors, fmt.Errorf("failed to persist alert: %w", err))
		} else {
			slog.Info("Persisted alert with pending destinations", "alert_id", alertID, "pending_destinations", unsent.PendingDestinations, "component", "alert")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to send alert to some destinations: %v", errors)
	}
	return nil
}

// sendToDestination sends an alert to a specific destination.
func (a *AlertBot) sendToDestination(ctx context.Context, dest string, alert *UnsentAlert) error {
	switch dest {
	case "telegram":
		if a.Bot == nil {
			return fmt.Errorf("telegram bot not initialized")
		}
		message := a.FormatAlert(alert.Title, alert.Event, alert.Details, alert.HostIP, alert.AlertType)
		if message == "" {
			return fmt.Errorf("empty alert message")
		}
		msg := tgbotapi.NewMessage(a.ChatID, message)
		msg.ParseMode = tgbotapi.ModeMarkdownV2
		_, err := a.Bot.Send(msg)
		return err
	case "web":
		if a.MonitorWebURL == "" {
			return fmt.Errorf("monitor-web URL not configured")
		}
		event := AlertEvent{
			Timestamp:   alert.Timestamp,
			Module:      alert.Module,
			ServiceName: alert.Title,
			EventName:   alert.Event,
			Details:     alert.Details,
			HostIP:      alert.HostIP,
			AlertType:   alert.AlertType,
			ClusterName: a.ClusterName,
			Hostname:    a.Hostname,
		}
		if alert.SpecificFields != nil {
			if bigKeysCount, ok := alert.SpecificFields["big_keys_count"].(int); ok {
				event.BigKeysCount = &bigKeysCount
			}
			if failedNodes, ok := alert.SpecificFields["failed_nodes"].(string); ok {
				event.FailedNodes = &failedNodes
			}
			if deadlocksInc, ok := alert.SpecificFields["deadlocks_increment"].(int64); ok {
				event.DeadlocksInc = &deadlocksInc
			}
			if slowQueriesInc, ok := alert.SpecificFields["slow_queries_increment"].(int64); ok {
				event.SlowQueriesInc = &slowQueriesInc
			}
			if connections, ok := alert.SpecificFields["connections"].(int); ok {
				event.Connections = &connections
			}
			if cpuUsage, ok := alert.SpecificFields["cpu_usage"].(float64); ok {
				event.CPUUsage = &cpuUsage
			}
			if memRemaining, ok := alert.SpecificFields["mem_remaining"].(float64); ok {
				event.MemRemaining = &memRemaining
			}
			if diskUsage, ok := alert.SpecificFields["disk_usage"].(float64); ok {
				event.DiskUsage = &diskUsage
			}
			if addedUsers, ok := alert.SpecificFields["added_users"].(string); ok {
				event.AddedUsers = &addedUsers
			}
			if removedUsers, ok := alert.SpecificFields["removed_users"].(string); ok {
				event.RemovedUsers = &removedUsers
			}
			if addedProcesses, ok := alert.SpecificFields["added_processes"].(string); ok {
				event.AddedProcesses = &addedProcesses
			}
			if removedProcesses, ok := alert.SpecificFields["removed_processes"].(string); ok {
				event.RemovedProcesses = &removedProcesses
			}
		}
		return a.sendToMonitorWeb(ctx, event)
	default:
		return fmt.Errorf("unknown destination: %s", dest)
	}
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
func (a *AlertBot) persistAlert(alert UnsentAlert) error {
	a.fileMutex.Lock()
	defer a.fileMutex.Unlock()

	filePath := "unsent_alerts.json"
	var alerts []UnsentAlert

	data, err := os.ReadFile(filePath)
	if err == nil {
		if err := json.Unmarshal(data, &alerts); err != nil {
			return fmt.Errorf("failed to unmarshal existing alerts: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read unsent alerts file: %w", err)
	}

	alerts = append(alerts, alert)

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

// LoadAndRetryUnsentAlerts loads unsent alerts and retries sending them to pending destinations.
func (a *AlertBot) LoadAndRetryUnsentAlerts(ctx context.Context) error {
	if a.MonitorWebURL == "" && a.Bot == nil {
		slog.Debug("No destinations configured, skipping unsent alerts retry", "component", "alert")
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

	var alerts []UnsentAlert
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
	var remaining []UnsentAlert

	for _, alert := range alerts {
		select {
		case <-ctx.Done():
			mu.Lock()
			remaining = append(remaining, alert)
			mu.Unlock()
			continue
		case sem <- struct{}{}:
			wg.Add(1)
			go func(alert UnsentAlert) {
				defer wg.Done()
				defer func() { <-sem }()

				for i := len(alert.PendingDestinations) - 1; i >= 0; i-- {
					dest := alert.PendingDestinations[i]
					for attempt := 1; attempt <= a.RetryTimes; attempt++ {
						taskCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
						defer cancel()

						if err := a.sendToDestination(taskCtx, dest, &alert); err == nil {
							slog.Info("Sent unsent alert to destination", "alert_id", alert.ID, "destination", dest, "service_name", alert.Title, "event_name", alert.Event, "attempt", attempt, "component", "alert")
							alert.PendingDestinations = append(alert.PendingDestinations[:i], alert.PendingDestinations[i+1:]...)
							break
						}
						slog.Error("Failed to send unsent alert to destination", "alert_id", alert.ID, "destination", dest, "error", err, "service_name", alert.Title, "event_name", alert.Event, "attempt", attempt, "component", "alert")
						if attempt < a.RetryTimes {
							select {
							case <-ctx.Done():
								break
							case <-time.After(a.RetryDelay):
								continue
							}
						}
					}
				}
				if len(alert.PendingDestinations) > 0 {
					mu.Lock()
					remaining = append(remaining, alert)
					mu.Unlock()
				}
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