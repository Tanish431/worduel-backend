package game

import (
	"context"
	"log"
	"time"

	"github.com/Tanish431/worduel/internal/models"
	"github.com/google/uuid"
)

const drainInterval = time.Second

func (s *Service) StartDrain(ctx context.Context, matchID uuid.UUID) {
	ticker := time.NewTicker(drainInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			done := s.drainTick(ctx, matchID)
			if done {
				return
			}
		}
	}
}

func (s *Service) drainTick(ctx context.Context, matchID uuid.UUID) bool {
	var match models.Match
	err := s.db.QueryRow(ctx,
		`SELECT id, player_a_id, player_b_id, status, player_a_hp, player_b_hp
		FROM matches WHERE id=$1`, matchID,
	).Scan(&match.ID, &match.PlayerAID, &match.PlayerBID, &match.Status,
		&match.PlayerAHP, &match.PlayerBHP)

	if err != nil {
		log.Printf("drain tick fetch: %v", err)
		return true
	}

	if match.Status != models.MatchActive {
		return true
	}

	newAHP := match.PlayerAHP - models.PassiveDrainRate
	newBHP := match.PlayerBHP - models.PassiveDrainRate

	_, err = s.db.Exec(ctx,
		`UPDATE matches SET player_a_hp=$1, player_b_hp=$2 WHERE id=$3`,
		newAHP, newBHP, matchID,
	)
	if err != nil {
		log.Printf("drain tick update: %v", err)
		return true
	}

	s.hub.SendToMatch(matchID, models.WSEvent{
		Type: models.EventHPUpdate,
		Payload: map[string]any{
			"player_a_hp": newAHP,
			"player_b_hp": newBHP,
		},
	})

	if newAHP <= 0 && newBHP <= 0 {
		go s.resolveMatch(ctx, &match, nil)
		return true
	}
	if newAHP <= 0 {
		go s.resolveMatch(ctx, &match, &match.PlayerBID)
		return true
	}
	if newBHP <= 0 {
		go s.resolveMatch(ctx, &match, &match.PlayerAID)
		return true
	}

	return false
}
