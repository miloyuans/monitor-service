package alert

import (
	"fmt"
	"log/slog"
	"os"
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
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		return nil, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		slog.Warn("Failed to get hostname", "error", err, "component", "alert")
		hostname = "unknown"
	}
	return &AlertBot{
		Bot:          bot,
		ChatID:       chatID,
		ClusterName:  clusterName,
		Hostname:     hostname,
		ShowHostname: showHostname,
	}, nil
}

// FormatAlert creates a standardized Markdown alert message.
func (a *AlertBot) FormatAlert(serviceName, eventName, details, hostIP string, alertType string) string {
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
	return fmt.Sprintf("%s\n*时间*: %s\n*环境*: %s\n*主机名*: %s\n*主机IP*: %s\n*服务名*: %s\n*事件名*: %s\n*详情*:\n%s",
		header,
		timestamp,
		a.ClusterName,
		hostname,
		hostIP,
		serviceName,
		eventName,
		details,
	)
}

// SendAlert sends a Telegram alert with the provided service name, event name, details, and host IP.
func (a *AlertBot) SendAlert(serviceName, eventName, details, hostIP string, alertType string) error {
	message := a.FormatAlert(serviceName, eventName, details, hostIP, alertType)
	msg := tgbotapi.NewMessage(a.ChatID, message)
	msg.ParseMode = "Markdown"
	if _, err := a.Bot.Send(msg); err != nil {
		slog.Error("Failed to send Telegram alert", "error", err, "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
		return err
	}
	slog.Info("Sent Telegram alert", "service_name", serviceName, "event_name", eventName, "host_ip", hostIP, "component", "alert")
	return nil
}