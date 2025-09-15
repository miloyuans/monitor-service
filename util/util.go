package util

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"monitor-service/alert"
)

// SendAlert sends a deduplicated alert with consistent logging.
func SendAlert(ctx context.Context, bot *alert.AlertBot, alertCache map[string]time.Time, cacheMutex *sync.Mutex, alertSilenceDuration time.Duration, serviceName, eventName, details, hostIP, alertType, module string, specificFields map[string]interface{}) error {
	// Ensure alert_key is set
	if _, ok := specificFields["alert_key"]; !ok {
		specificFields["alert_key"] = fmt.Sprintf("%s_%s_%s", module, alertType, hostIP)
	}

	hash, err := MD5Hash(details)
	if err != nil {
		slog.Error("Failed to generate alert hash", "error", err, "service_name", serviceName, "module", module, "component", "util")
		return fmt.Errorf("failed to generate alert hash: %w", err)
	}

	cacheMutex.Lock()
	now := time.Now()
	if timestamp, ok := alertCache[hash]; ok && now.Sub(timestamp) < alertSilenceDuration {
		slog.Debug("Skipping duplicate alert", "hash", hash, "service_name", serviceName, "module", module, "component", "util")
		cacheMutex.Unlock()
		return nil
	}
	alertCache[hash] = now
	// Clean up expired cache entries
	for h, t := range alertCache {
		if now.Sub(t) >= alertSilenceDuration {
			delete(alertCache, h)
			slog.Debug("Removed expired alert cache entry", "hash", h, "service_name", serviceName, "module", module, "component", "util")
		}
	}
	cacheMutex.Unlock()

	slog.Debug("Sending alert", "service_name", serviceName, "event_name", eventName, "module", module, "details", details, "component", "util")
	if err := bot.SendAlert(ctx, serviceName, eventName, details, hostIP, alertType, module, specificFields); err != nil {
		slog.Error("Failed to send alert", "error", err, "service_name", serviceName, "event_name", eventName, "module", module, "details", details, "component", "util")
		return fmt.Errorf("failed to send alert: %w", err)
	}
	slog.Info("Sent alert", "service_name", serviceName, "event_name", eventName, "module", module, "details", details, "component", "util")
	return nil
}

// GetPrivateIP retrieves the first non-loopback IPv4 address in private ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16).
func GetPrivateIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		slog.Error("Failed to retrieve network interfaces", "error", err, "component", "util")
		return "", fmt.Errorf("failed to retrieve network interfaces: %w", err)
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			if isPrivateIP(ipnet.IP) {
				slog.Debug("Found private IP", "ip", ipnet.IP.String(), "component", "util")
				return ipnet.IP.String(), nil
			}
		}
	}

	slog.Warn("No private IPv4 address found", "component", "util")
	return "", nil
}

// isPrivateIP checks if an IP is in a private range (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16).
func isPrivateIP(ip net.IP) bool {
	privateRanges := []struct {
		start, end net.IP
	}{
		{net.ParseIP("10.0.0.0"), net.ParseIP("10.255.255.255")},
		{net.ParseIP("172.16.0.0"), net.ParseIP("172.31.255.255")},
		{net.ParseIP("192.168.0.0"), net.ParseIP("192.168.255.255")},
	}
	for _, r := range privateRanges {
		if bytesCompare(ip, r.start) >= 0 && bytesCompare(ip, r.end) <= 0 {
			return true
		}
	}
	return false
}

// bytesCompare compares two IP addresses as byte slices.
func bytesCompare(a, b net.IP) int {
	for i := 0; i < len(a); i++ {
		if a[i] < b[i] {
			return -1
		} else if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// MD5Hash generates an MD5 hash of the input string.
func MD5Hash(input string) (string, error) {
	hash := md5.Sum([]byte(input))
	return hex.EncodeToString(hash[:]), nil
}