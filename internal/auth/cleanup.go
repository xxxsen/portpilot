package auth

import "time"

func (s *ChallengeStore) pruneChallengesLocked(now time.Time) {
	for id, challenge := range s.challenges {
		if now.After(challenge.ExpiresAt) {
			delete(s.challenges, id)
		}
	}
}

func (s *TicketStore) pruneTicketsLocked(now time.Time) {
	for id, ticket := range s.tickets {
		if now.After(ticket.ExpiresAt) {
			delete(s.tickets, id)
		}
	}
}
