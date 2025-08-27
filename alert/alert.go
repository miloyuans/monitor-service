package alert

import (
	"fmt"
	"os"
	"strings"
	"text/template"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// AlertBot wraps the Telegram bot and alert template.
type AlertBot struct {
	bot          *tgbotapi.BotAPI
	chatID       int64
	clusterName  string
	showHostname bool
	hostname     string
	template     *template.Template
}

// AlertData holds the data for rendering the alert template.
type AlertData struct {
	ClusterName string
	Hostname    string
	Service     string
	Issue       string
	Details     string
	ShowHostname bool
}

// NewAlertBot initializes a new Telegram bot for sending alerts.
func NewAlertBot(token string, chatID int64, clusterName string, showHostname bool) (*AlertBot, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("failed to create Telegram bot: %w", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		fmt.Printf("Warning: Failed to get hostname: %v\n", err)
		hostname = "unknown"
	}
	tmpl := template.Must(template.New("alert").Parse(`
🚨 *Service Alert* 🚨

*Cluster*: {{.ClusterName}}
{{if .ShowHostname}}*Hostname*: {{.Hostname}}{{end}}
*Service*: {{.Service}}
*Issue*: {{.Issue}}
*Details*: 
{{.Details}}
`))
	return &AlertBot{
		bot:          bot,
		chatID:       chatID,
		clusterName:  clusterName,
		showHostname: showHostname,
		hostname:     hostname,
		template:     tmpl,
	}, nil
}

// SendAlert sends a formatted alert to Telegram using the Markdown template.
func (a *AlertBot) SendAlert(message string) {
	lines := strings.Split(message, "\n\n")
	for _, line := range lines {
		parts := strings.SplitN(line, ": ", 2)
		if len(parts) < 2 {
			continue
		}
		service := strings.Trim(parts[0], "**")
		data := AlertData{
			ClusterName:  a.clusterName,
			Hostname:     a.hostname,
			Service:      service,
			Issue:        "Service Failure",
			Details:      parts[1],
			ShowHostname: a.showHostname,
		}
		var buf strings.Builder
		if err := a.template.Execute(&buf, data); err != nil {
			fmt.Printf("Error rendering alert template: %v\n", err)
			continue
		}
		msg := tgbotapi.NewMessage(a.chatID, buf.String())
		msg.ParseMode = tgbotapi.ModeMarkdown
		if _, err := a.bot.Send(msg); err != nil {
			fmt.Printf("Error sending alert: %v\n", err)
		}
	}
}