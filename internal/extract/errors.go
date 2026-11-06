package extract

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// FailureClass is deliberately a closed, content-free vocabulary for observers.
func FailureClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "timeout") {
		return "timeout"
	}
	message := err.Error()
	if strings.Contains(message, "index:") || strings.Contains(message, "neighbors.") || strings.Contains(message, "index busy") {
		return "index"
	}
	var status int
	if _, e := fmt.Sscanf(message, "extraction HTTP status %d", &status); e == nil && status >= 100 && status <= 599 {
		return fmt.Sprintf("provider_http_%d", status)
	}
	if strings.Contains(message, "credential") || strings.Contains(message, "env file") {
		return "no_key"
	}
	if strings.Contains(message, "HTTP request failed") {
		return "provider_network"
	}
	if strings.Contains(message, "response") || strings.Contains(message, "parse") {
		return "parse"
	}
	if strings.Contains(message, "scope") || strings.Contains(message, "binding") || strings.Contains(message, "marker") {
		return "scope"
	}
	if strings.Contains(message, "provider") || strings.Contains(message, "base URL") {
		return "config"
	}
	return "capture"
}
