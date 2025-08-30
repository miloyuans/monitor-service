package alert

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// AlertBot handles sending alerts via Telegram.
type AlertBot struct {
	Bot          *tgbotapi.BotAPI
	ChatID       int64
	ClusterName  string
	Hostname     string
	ShowHostname bool
}

// NewAlertBot creates a new Telegram bot for alerts.
func NewAlertBot(botToken string, chatID int64, clusterName string, showHostname bool) (*AlertBot, error) {
	if botToken == "" {
		slog.Error("Bot token is empty", "component", "alert")
		return nil, fmt.Errorf("bot token is empty")
	}
	if chatID == 0 {
		slog.Error("Chat ID is invalid", "component", "alert")
		return nil, fmt.Errorf("invalid chat ID")
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
	slog.Info("Initialized alert bot", "cluster_name", clusterName, "show_hostname", showHostname, "component", "alert")
	return &AlertBot{
		Bot:          bot,
		ChatID:       chatID,
		ClusterName:  clusterName,
		Hostname:     hostname,
		ShowHostname: showHostname,
	}, nil
}

// FormatAlert creates a standardized Markdown alert message.
func (a *AlertBot) FormatAlert(serviceName, eventName, details, hostIP, alertType string) string {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	hostname := a.Hostname
	if !a.ShowHostname {
		hostname = "N/A"
	}
	var header string
	switch alertType {
	case "startup":
		header = "🚀 *监控服务启动通知 Monitoring Service Startup* 🚀"
	case "shutdown":
		header = "🛑 *监控服务关闭通知 Monitoring Service Shutdown* 🛑"
	default:
		header = "🚨 *监控 Monitoring 告警 Alert* 🚨"
	}

	// Escape all fields for MarkdownV2 to prevent parsing errors
	header = EscapeMarkdown(header)
	timestamp = EscapeMarkdown(timestamp)
	clusterName := EscapeMarkdown(a.ClusterName)
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

// SendAlert sends a Telegram alert with the provided service name, event name, details, and host IP.
func (a *AlertBot) SendAlert(ctx context.Context, serviceName, eventName, details, hostIP, alertType string) error {
	message := a.FormatAlert(serviceName, eventName, details, hostIP, alertType)
	msg := tgbotapi.NewMessage(a.ChatID, message)
	msg.ParseMode = tgbotapi.ModeMarkdownV2 // Use MarkdownV2 for better escaping support

	// Send message with context
	ch := make(chan error, 1)
	go func() {
		_, err := a.Bot.Send(msg)
		ch <- err
	}()

	select {
	case <-ctx.Done():
		slog.Warn("Alert sending cancelled", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
		return ctx.Err()
	case err := <-ch:
		if err != nil {
			slog.Error("Failed to send Telegram alert", "error", err, "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "alert_type", alertType, "component", "alert")
			return fmt.Errorf("failed to send Telegram alert: %w", err)
		}
		slog.Info("Sent Telegram alert", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "alert_type", alertType, "component", "alert")
		return nil
	}
}

// EscapeMarkdown escapes Telegram MarkdownV2 special characters to prevent formatting issues.
func EscapeMarkdown(text string) string {
	specialChars := []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}
	for _, char := range specialChars {
		text = strings.ReplaceAll(text, char, "\\"+char)
	}
	return text
}