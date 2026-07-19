package server

import (
	"github.com/portpilot/portpilot/internal/config"
)

func rotatedClientTokens(current, next config.ClientFile) []string {
	existing := make(map[string]string, len(current.ClientConfig))
	for _, client := range current.ClientConfig {
		existing[client.ClientID] = client.ClientToken
	}
	rotated := make([]string, 0)
	for _, client := range next.ClientConfig {
		previous, exists := existing[client.ClientID]
		if !exists || client.ClientToken == config.RedactionMarker {
			continue
		}
		if client.ClientToken != previous {
			rotated = append(rotated, client.ClientID)
		}
	}
	return rotated
}
