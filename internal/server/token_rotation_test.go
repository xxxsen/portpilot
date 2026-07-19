package server

import (
	"slices"
	"testing"

	"github.com/portpilot/portpilot/internal/config"
)

func TestRotatedClientTokens(t *testing.T) {
	t.Parallel()
	current := config.ClientFile{ClientConfig: []config.ClientConfig{
		{ClientID: "unchanged", ClientToken: "old-a"},
		{ClientID: "redacted", ClientToken: "old-b"},
		{ClientID: "rotated", ClientToken: "old-c"},
	}}
	next := config.ClientFile{ClientConfig: []config.ClientConfig{
		{ClientID: "unchanged", ClientToken: "old-a"},
		{ClientID: "redacted", ClientToken: config.RedactionMarker},
		{ClientID: "rotated", ClientToken: "new-c"},
		{ClientID: "new", ClientToken: "new-client-token"},
	}}
	if got := rotatedClientTokens(current, next); !slices.Equal(got, []string{"rotated"}) {
		t.Fatalf("unexpected token rotations: %v", got)
	}
}
