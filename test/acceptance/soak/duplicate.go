package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/protocol"
)

const (
	defaultControlURL  = "ws://127.0.0.1:6100/portpilot/control"
	defaultClientID    = "dev-client"
	defaultClientToken = "cG9ydHBpbG90LWRldi10b2tlbi0wMTIzNDU2Nzg5YWJjZGVm" //nolint:gosec // Public local-only fixture credential.
)

func replaceControlSession(ctx context.Context) (uint64, error) {
	controlURL := envString("PORTPILOT_DEV_CONTROL_URL", defaultControlURL)
	clientID := envString("PORTPILOT_DEV_CLIENT_ID", defaultClientID)
	token := envString("PORTPILOT_DEV_CLIENT_TOKEN", defaultClientToken)
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, controlURL, http.Header{
		"User-Agent": []string{"portpilot-soak-test/1"},
	})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return 0, fmt.Errorf("dial duplicate control: %w", err)
	}
	defer func() { _ = conn.Close() }()

	nonce, err := auth.RandomToken(18)
	if err != nil {
		return 0, err
	}
	init, _ := protocol.NewMessage(protocol.TypeAuthInit, protocol.AuthInit{
		ClientID: clientID, Role: "control", ClientNonce: nonce,
	})
	if err := conn.WriteJSON(init); err != nil {
		return 0, err
	}
	var challengeMessage protocol.Message
	if err := conn.ReadJSON(&challengeMessage); err != nil {
		return 0, err
	}
	if challengeMessage.Type != protocol.TypeAuthChallenge {
		return 0, fmt.Errorf("duplicate control expected AUTH_CHALLENGE, got %s", challengeMessage.Type)
	}
	challenge, err := protocol.DecodePayload[protocol.AuthChallenge](challengeMessage)
	if err != nil {
		return 0, err
	}
	signature, err := auth.Signature(
		token,
		clientID,
		"control",
		challenge.ChallengeID,
		challenge.Challenge,
		nonce,
	)
	if err != nil {
		return 0, err
	}
	proof, _ := protocol.NewMessage(protocol.TypeAuthProof, protocol.AuthProof{
		ChallengeID: challenge.ChallengeID,
		Signature:   signature,
	})
	if err := conn.WriteJSON(proof); err != nil {
		return 0, err
	}
	var result protocol.Message
	if err := conn.ReadJSON(&result); err != nil {
		return 0, err
	}
	if result.Type != protocol.TypeAuthOK {
		return 0, fmt.Errorf("duplicate control authentication returned %s", result.Type)
	}
	ok, err := protocol.DecodePayload[protocol.AuthOK](result)
	if err != nil {
		return 0, err
	}
	return ok.SessionEpoch, nil
}
