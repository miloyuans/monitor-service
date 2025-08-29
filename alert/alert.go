package alert

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// AlertBot handles sending alerts via Telegram.
type AlertBot struct {
	Bot         *tgbotapi.BotAPI
	ChatID      int64
	ClusterName string
	Hostname    string
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
		slog.Warn("Failed to get hostname", "error", err)
		hostname = "unknown"
	}
	return &AlertBot{
		Bot:         bot,
		ChatID:      chatID,
		ClusterName: clusterName,
		Hostname:    hostname,
		ShowHostname: showHostname,
	}, nil
}

// SendAlert sends a Telegram alert with the provided message and host IP.
func (a *AlertBot) SendAlert(message, hostIP string) {
	msg := tgbotapi.NewMessage(a.ChatID, message)
	msg.ParseMode = "Markdown"
	if _, err := a.Bot.Send(msg); err != nil {
		slog.Error("Failed to send Telegram alert", "error", err)
	} else {
		slog.Info("Sent Telegram alert", "message", message, "hostIP", hostIP)
	}
}