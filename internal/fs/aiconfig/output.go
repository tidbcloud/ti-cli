package aiconfig

import (
	"fmt"
	"strings"
	"time"
)

func (r ExtractResult) Human() string {
	return strings.Join([]string{
		"File system ID: " + r.FileSystemID,
		"Media type: " + r.MediaType,
		fmt.Sprintf("Enabled: %t", r.Enabled),
		"Source: " + valueOrNone(r.Source),
		"Provider API base: " + pointerOrNone(r.APIBase),
		"Provider API key: " + pointerOrNone(r.APIKey),
		"Provider model: " + pointerOrNone(r.Model),
		"Provider protocol: " + pointerOrNone(r.Protocol),
		"Prompt: " + pointerOrNone(r.Prompt),
		"Updated at: " + timeOrNone(r.UpdatedAt),
	}, "\n")
}

func (r EmbeddingResult) Human() string {
	return strings.Join([]string{
		"File system ID: " + r.FileSystemID,
		fmt.Sprintf("Enabled: %t", r.Enabled),
		"Source: " + valueOrNone(r.Source),
		"Provider API base: " + pointerOrNone(r.APIBase),
		"Provider API key: " + pointerOrNone(r.APIKey),
		"Provider model: " + pointerOrNone(r.Model),
		fmt.Sprintf("Generation: %d", r.Generation),
		"Updated at: " + timeOrNone(r.UpdatedAt),
	}, "\n")
}

func pointerOrNone(value *string) string {
	if value == nil || *value == "" {
		return "none"
	}
	return *value
}

func valueOrNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func timeOrNone(value *time.Time) string {
	if value == nil {
		return "none"
	}
	return value.Format(time.RFC3339)
}
