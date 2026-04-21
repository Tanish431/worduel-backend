package models

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID             uuid.UUID `json:"id"`
	Username       string    `json:"username"`
	Email          string    `json:"email,omitempty"`
	PasswordHash   string    `json:"-"`
	AuthProvider   string    `json:"-"`
	ProviderUserID *string   `json:"-"`
	ELO            int       `json:"elo"`
	CreatedAt      time.Time `json:"created_at"`
}

type MatchStatus string

const (
	MatchPending  MatchStatus = "pending"
	MatchActive   MatchStatus = "active"
	MatchFinished MatchStatus = "finished"
)

type Match struct {
	ID             uuid.UUID   `json:"id"`
	PlayerAID      uuid.UUID   `json:"player_a_id"`
	PlayerBID      uuid.UUID   `json:"player_b_id"`
	Status         MatchStatus `json:"status"`
	WinnerID       *uuid.UUID  `json:"winner_id,omitempty"`
	PlayerAHP      int         `json:"player_a_hp"`
	PlayerBHP      int         `json:"player_b_hp"`
	PlayerAWordIdx int         `json:"player_a_word_idx"`
	PlayerBWordIdx int         `json:"player_b_word_idx"`
	IsRanked       bool        `json:"is_ranked"`
	StartedAt      time.Time   `json:"started_at"`
	FinishedAt     *time.Time  `json:"finished_at,omitempty"`
}

type TileResult string

const (
	TileCorrect TileResult = "correct"
	TilePresent TileResult = "present"
	TileAbsent  TileResult = "absent"
)

type Guess struct {
	ID        uuid.UUID    `json:"id"`
	MatchID   uuid.UUID    `json:"match_id"`
	PlayerID  uuid.UUID    `json:"player_id"`
	WordIndex int          `json:"word_index"`
	Guess     string       `json:"guess"`
	Result    []TileResult `json:"result"`
	GuessedAt time.Time    `json:"guessed_at"`
}

type WSEvent struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

const (
	EventMatchFound        = "match_found"
	EventGuessResult       = "guess_result"
	EventOpponentGuess     = "opponent_guess"
	EventWordSolved        = "word_solved"
	EventHPUpdate          = "hp_update"
	EventMatchOver         = "match_over"
	EventOpponentLeft      = "opponent_left"
	EventGuessReset        = "guess_reset"
	EventChallengeRequest  = "challenge_request"
	EventChallengeAccepted = "challenge_accepted"
	EventChallengeDeclined = "challenge_declined"
	EventRematchRequest    = "rematch_request"
	EventRematchAccepted   = "rematch_accepted"
	EventRematchDeclined   = "rematch_declined"
)

const (
	StartingHP         = 100
	PassiveDrainRate   = 1
	SolveBonus         = 15
	FailPenalty        = 5
	PartialHealPerTile = 2
)
