package auth

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const authLabel = "portpilot-auth-v1"

type Challenge struct {
	ID          string
	ClientID    string
	Role        string
	ClientNonce string
	Value       string
	ExpiresAt   time.Time
}

type ChallengeStore struct {
	mu         sync.Mutex
	challenges map[string]Challenge
	ttl        time.Duration
}

func NewChallengeStore(ttl time.Duration) *ChallengeStore {
	return &ChallengeStore{challenges: make(map[string]Challenge), ttl: ttl}
}

func (s *ChallengeStore) Create(clientID, role, clientNonce string) (Challenge, error) {
	id, err := RandomToken(18)
	if err != nil {
		return Challenge{}, err
	}
	value, err := RandomToken(32)
	if err != nil {
		return Challenge{}, err
	}
	challenge := Challenge{
		ID: id, ClientID: clientID, Role: role, ClientNonce: clientNonce,
		Value: value, ExpiresAt: time.Now().Add(s.ttl),
	}
	s.mu.Lock()
	s.pruneChallengesLocked(time.Now())
	s.challenges[id] = challenge
	s.mu.Unlock()
	return challenge, nil
}

func (s *ChallengeStore) Consume(id string) (Challenge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	challenge, ok := s.challenges[id]
	delete(s.challenges, id)
	if !ok || time.Now().After(challenge.ExpiresAt) {
		return Challenge{}, false
	}
	return challenge, true
}

func Signature(token, clientID, role, challengeID, challenge, clientNonce string) (string, error) {
	key, err := deriveKey(token, clientID)
	if err != nil {
		return "", err
	}
	canonical := strings.Join([]string{
		authLabel, clientID, role, challengeID, challenge, clientNonce,
	}, "\n")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func VerifySignature(token string, challenge Challenge, signature string) bool {
	expected, err := Signature(
		token, challenge.ClientID, challenge.Role, challenge.ID,
		challenge.Value, challenge.ClientNonce,
	)
	if err != nil || len(expected) != len(signature) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

func deriveKey(token, clientID string) ([]byte, error) {
	reader, err := hkdf.Key(sha256.New, []byte(token), []byte(authLabel), clientID, 32)
	if err != nil {
		return nil, fmt.Errorf("derive authentication key: %w", err)
	}
	return reader, nil
}

func RandomToken(bytes int) (string, error) {
	if bytes < 1 {
		return "", errors.New("token size must be positive")
	}
	data := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return "", fmt.Errorf("read random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

type TicketBinding struct {
	ClientID     string
	SessionID    string
	SessionEpoch uint64
	Kind         string
	ObjectID     string
}

type ticketRecord struct {
	Hash      [32]byte
	Binding   TicketBinding
	ExpiresAt time.Time
}

type TicketStore struct {
	mu      sync.Mutex
	tickets map[string]ticketRecord
	ttl     time.Duration
}

func NewTicketStore(ttl time.Duration) *TicketStore {
	return &TicketStore{tickets: make(map[string]ticketRecord), ttl: ttl}
}

func (s *TicketStore) Issue(binding TicketBinding) (string, error) {
	id, err := RandomToken(18)
	if err != nil {
		return "", err
	}
	secret, err := RandomToken(32)
	if err != nil {
		return "", err
	}
	value := id + "." + secret
	s.mu.Lock()
	s.pruneTicketsLocked(time.Now())
	s.tickets[id] = ticketRecord{
		Hash: sha256.Sum256([]byte(value)), Binding: binding,
		ExpiresAt: time.Now().Add(s.ttl),
	}
	s.mu.Unlock()
	return value, nil
}

func (s *TicketStore) Consume(value string, expected TicketBinding) bool {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	s.mu.Lock()
	record, ok := s.tickets[parts[0]]
	delete(s.tickets, parts[0])
	s.mu.Unlock()
	if !ok || time.Now().After(record.ExpiresAt) || record.Binding != expected {
		return false
	}
	actual := sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(actual[:], record.Hash[:]) == 1
}

func (s *TicketStore) InvalidateSession(clientID, sessionID string, epoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ticket := range s.tickets {
		b := ticket.Binding
		if b.ClientID == clientID && b.SessionID == sessionID && b.SessionEpoch == epoch {
			delete(s.tickets, id)
		}
	}
}

func (b TicketBinding) String() string {
	return fmt.Sprintf("%s/%s/%d/%s/%s", b.ClientID, b.SessionID, b.SessionEpoch, b.Kind, b.ObjectID)
}
