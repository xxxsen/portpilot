package auth

import (
	"testing"
	"time"
)

func TestChallengeSignatureAndSingleConsumption(t *testing.T) {
	t.Parallel()
	store := NewChallengeStore(time.Second)
	challenge, err := store.Create("client-a", "control", "nonce")
	if err != nil {
		t.Fatal(err)
	}
	signature, err := Signature(
		"01234567890123456789012345678901234567890123",
		challenge.ClientID, challenge.Role, challenge.ID,
		challenge.Value, challenge.ClientNonce,
	)
	if err != nil {
		t.Fatal(err)
	}
	consumed, ok := store.Consume(challenge.ID)
	if !ok {
		t.Fatal("challenge was not consumable")
	}
	if !VerifySignature("01234567890123456789012345678901234567890123", consumed, signature) {
		t.Fatal("valid signature was rejected")
	}
	if VerifySignature("different-token-012345678901234567890123456789", consumed, signature) {
		t.Fatal("signature verified with a different token")
	}
	if _, ok := store.Consume(challenge.ID); ok {
		t.Fatal("challenge replay was accepted")
	}
}

func TestTicketBindingExpiryAndReplay(t *testing.T) {
	t.Parallel()
	store := NewTicketStore(50 * time.Millisecond)
	binding := TicketBinding{
		ClientID: "client-a", SessionID: "session-a",
		SessionEpoch: 3, Kind: "tcp", ObjectID: "connection-a",
	}
	ticket, err := store.Issue(binding)
	if err != nil {
		t.Fatal(err)
	}
	wrong := binding
	wrong.SessionEpoch++
	if store.Consume(ticket, wrong) {
		t.Fatal("ticket with a wrong binding was accepted")
	}
	if store.Consume(ticket, binding) {
		t.Fatal("ticket consumed by a failed binding check was replayable")
	}

	expiring, err := store.Issue(binding)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(75 * time.Millisecond)
	if store.Consume(expiring, binding) {
		t.Fatal("expired ticket was accepted")
	}

	valid, err := store.Issue(binding)
	if err != nil {
		t.Fatal(err)
	}
	if !store.Consume(valid, binding) {
		t.Fatal("valid ticket was rejected")
	}
	if store.Consume(valid, binding) {
		t.Fatal("ticket replay was accepted")
	}
}
