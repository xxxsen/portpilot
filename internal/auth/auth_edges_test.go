package auth

import (
	"testing"
	"time"
)

func TestChallengeExpiryAndCanonicalBinding(t *testing.T) {
	t.Parallel()
	store := NewChallengeStore(15 * time.Millisecond)
	challenge, err := store.Create("client-a", "control", "nonce-a")
	if err != nil {
		t.Fatal(err)
	}
	signature, err := Signature(
		"01234567890123456789012345678901234567890123",
		challenge.ClientID, challenge.Role, challenge.ID, challenge.Value, challenge.ClientNonce,
	)
	if err != nil {
		t.Fatal(err)
	}
	tampered := challenge
	tampered.ClientNonce = "nonce-b"
	if VerifySignature("01234567890123456789012345678901234567890123", tampered, signature) {
		t.Fatal("signature accepted with a different client nonce")
	}
	time.Sleep(25 * time.Millisecond)
	if _, ok := store.Consume(challenge.ID); ok {
		t.Fatal("expired challenge was consumed")
	}
}

func TestTicketsBindEverySessionDimension(t *testing.T) {
	t.Parallel()
	store := NewTicketStore(time.Second)
	base := TicketBinding{
		ClientID: "client", SessionID: "session", SessionEpoch: 4,
		Kind: "mux", ObjectID: "object",
	}
	tests := map[string]func(TicketBinding) TicketBinding{
		"client": func(binding TicketBinding) TicketBinding {
			binding.ClientID = "other"
			return binding
		},
		"session": func(binding TicketBinding) TicketBinding {
			binding.SessionID = "other"
			return binding
		},
		"epoch": func(binding TicketBinding) TicketBinding {
			binding.SessionEpoch++
			return binding
		},
		"kind": func(binding TicketBinding) TicketBinding {
			binding.Kind = "udp"
			return binding
		},
		"object": func(binding TicketBinding) TicketBinding {
			binding.ObjectID = "other"
			return binding
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ticket, err := store.Issue(base)
			if err != nil {
				t.Fatal(err)
			}
			if store.Consume(ticket, mutate(base)) {
				t.Fatal("ticket accepted with a mismatched binding")
			}
			if store.Consume(ticket, base) {
				t.Fatal("failed binding attempt did not atomically consume ticket")
			}
		})
	}
}
