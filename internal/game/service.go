package game

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Tanish431/worduel/internal/middleware"
	"github.com/Tanish431/worduel/internal/models"
	"github.com/Tanish431/worduel/internal/word"
	"github.com/Tanish431/worduel/internal/ws"
	"github.com/Tanish431/worduel/pkg/elo"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxGuesses = 6

type Service struct {
	db      *pgxpool.Pool
	wordSvc *word.Service
	hub     *ws.Hub
}

func NewService(db *pgxpool.Pool, wordSvc *word.Service, hub *ws.Hub) *Service {
	return &Service{db: db, wordSvc: wordSvc, hub: hub}
}

func (s *Service) HandleGetMatch(c *gin.Context) {
	matchID, err := uuid.Parse(c.Param("matchID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid match id"})
		return
	}

	var match models.Match
	err = s.db.QueryRow(c.Request.Context(),
		`SELECT id, player_a_id, player_b_id, status, winner_id, player_a_hp, player_b_hp,
	player_a_word_idx, player_b_word_idx, is_ranked, game_mode, started_at, finished_at FROM matches WHERE id=$1`, matchID,
	).Scan(&match.ID, &match.PlayerAID, &match.PlayerBID, &match.Status, &match.WinnerID,
		&match.PlayerAHP, &match.PlayerBHP, &match.PlayerAWordIdx, &match.PlayerBWordIdx, &match.IsRanked,
		&match.GameMode, &match.StartedAt, &match.FinishedAt)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "match not found"})
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if userID != match.PlayerAID && userID != match.PlayerBID {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a participant"})
		return
	}
	c.JSON(http.StatusOK, match)
}

type matchSummaryRound struct {
	WordIndex      int            `json:"word_index"`
	TargetWord     string         `json:"target_word"`
	PlayerAGuesses []models.Guess `json:"player_a_guesses"`
	PlayerBGuesses []models.Guess `json:"player_b_guesses"`
}

type matchSummaryPlayer struct {
	ID       uuid.UUID `json:"id"`
	Username string    `json:"username"`
}

type matchSummaryResponse struct {
	MatchID  uuid.UUID           `json:"match_id"`
	IsRanked bool                `json:"is_ranked"`
	WinnerID *uuid.UUID          `json:"winner_id,omitempty"`
	PlayerA  matchSummaryPlayer  `json:"player_a"`
	PlayerB  matchSummaryPlayer  `json:"player_b"`
	Rounds   []matchSummaryRound `json:"rounds"`
}

func (s *Service) HandleGetMatchSummary(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	matchID, err := uuid.Parse(c.Param("matchID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid match id"})
		return
	}

	var match models.Match
	var playerAUsername, playerBUsername string
	err = s.db.QueryRow(c.Request.Context(),
		`SELECT m.id, m.player_a_id, m.player_b_id, m.status, m.winner_id, m.player_a_hp, m.player_b_hp,
		        m.player_a_word_idx, m.player_b_word_idx, m.is_ranked, m.started_at, m.finished_at,
		        ua.username, ub.username
		 FROM matches m
		 JOIN users ua ON ua.id = m.player_a_id
		 JOIN users ub ON ub.id = m.player_b_id
		 WHERE m.id=$1`,
		matchID,
	).Scan(
		&match.ID, &match.PlayerAID, &match.PlayerBID, &match.Status, &match.WinnerID, &match.PlayerAHP, &match.PlayerBHP,
		&match.PlayerAWordIdx, &match.PlayerBWordIdx, &match.IsRanked, &match.StartedAt, &match.FinishedAt,
		&playerAUsername, &playerBUsername,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "match not found"})
		return
	}

	if userID != match.PlayerAID && userID != match.PlayerBID {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a participant"})
		return
	}

	rows, err := s.db.Query(c.Request.Context(),
		`SELECT id, match_id, player_id, word_index, guess, result, guessed_at
		 FROM guesses
		 WHERE match_id=$1
		 ORDER BY word_index ASC, guessed_at ASC`,
		matchID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load guesses"})
		return
	}
	defer rows.Close()

	roundsByIndex := make(map[int]*matchSummaryRound)
	roundIndices := make(map[int]struct{})

	for rows.Next() {
		var guess models.Guess
		var rawResult []byte
		if err := rows.Scan(&guess.ID, &guess.MatchID, &guess.PlayerID, &guess.WordIndex, &guess.Guess, &rawResult, &guess.GuessedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to scan guesses"})
			return
		}
		if err := json.Unmarshal(rawResult, &guess.Result); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to parse guesses"})
			return
		}
		roundIndices[guess.WordIndex] = struct{}{}

		round, exists := roundsByIndex[guess.WordIndex]
		if !exists {
			round = &matchSummaryRound{
				WordIndex:      guess.WordIndex,
				TargetWord:     s.wordSvc.GetWordAtIndex(c.Request.Context(), guess.WordIndex),
				PlayerAGuesses: []models.Guess{},
				PlayerBGuesses: []models.Guess{},
			}
			roundsByIndex[guess.WordIndex] = round
		}

		if guess.PlayerID == match.PlayerAID {
			round.PlayerAGuesses = append(round.PlayerAGuesses, guess)
		} else if guess.PlayerID == match.PlayerBID {
			round.PlayerBGuesses = append(round.PlayerBGuesses, guess)
		}
	}

	// Include exactly one additional word: the furthest upcoming word either player had reached.
	nextWordIndex := max(match.PlayerAWordIdx, match.PlayerBWordIdx)
	roundIndices[nextWordIndex] = struct{}{}
	if _, exists := roundsByIndex[nextWordIndex]; !exists {
		roundsByIndex[nextWordIndex] = &matchSummaryRound{
			WordIndex:      nextWordIndex,
			TargetWord:     s.wordSvc.GetWordAtIndex(c.Request.Context(), nextWordIndex),
			PlayerAGuesses: []models.Guess{},
			PlayerBGuesses: []models.Guess{},
		}
	}

	sortedIndices := make([]int, 0, len(roundIndices))
	for index := range roundIndices {
		sortedIndices = append(sortedIndices, index)
	}
	sort.Ints(sortedIndices)

	rounds := make([]matchSummaryRound, 0, len(roundsByIndex))
	for _, index := range sortedIndices {
		if round, exists := roundsByIndex[index]; exists {
			rounds = append(rounds, *round)
		}
	}

	c.JSON(http.StatusOK, matchSummaryResponse{
		MatchID:  match.ID,
		IsRanked: match.IsRanked,
		WinnerID: match.WinnerID,
		PlayerA: matchSummaryPlayer{
			ID:       match.PlayerAID,
			Username: playerAUsername,
		},
		PlayerB: matchSummaryPlayer{
			ID:       match.PlayerBID,
			Username: playerBUsername,
		},
		Rounds: rounds,
	})
}

type submitGuessRequest struct {
	Guess string `json:"guess" binding:"required,len=5"`
}

func (s *Service) HandleSubmitGuess(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	matchID, err := uuid.Parse(c.Param("matchID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid match id"})
		return
	}

	var req submitGuessRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "guess must be 5 letters"})
		return
	}
	req.Guess = strings.ToLower(req.Guess)

	if !s.wordSvc.IsValidGuess(c.Request.Context(), req.Guess) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "not in word list"})
		return
	}

	var match models.Match
	err = s.db.QueryRow(c.Request.Context(),
		`SELECT id, player_a_id, player_b_id, status,
	player_a_hp, player_b_hp, player_a_word_idx, player_b_word_idx, game_mode
	FROM matches WHERE id=$1`, matchID,
	).Scan(
		&match.ID, &match.PlayerAID, &match.PlayerBID, &match.Status,
		&match.PlayerAHP, &match.PlayerBHP, &match.PlayerAWordIdx, &match.PlayerBWordIdx, &match.GameMode,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "match not found"})
		return
	}
	if match.Status != models.MatchActive {
		c.JSON(http.StatusConflict, gin.H{"error": "match is not active"})
		return
	}

	isPlayerA := userID == match.PlayerAID
	if !isPlayerA && userID != match.PlayerBID {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a participant"})
		return
	}

	wordIdx := match.PlayerAWordIdx
	myHP := match.PlayerAHP
	opponentID := match.PlayerBID
	if !isPlayerA {
		wordIdx = match.PlayerBWordIdx
		myHP = match.PlayerBHP
		opponentID = match.PlayerAID
	}
	var alreadyGuessed bool
	s.db.QueryRow(c.Request.Context(),
		`SELECT EXISTS(
		SELECT 1 FROM guesses
		WHERE match_id=$1 AND player_id=$2 AND word_index=$3 AND guess=$4
	)`,
		matchID, userID, wordIdx, strings.ToLower(req.Guess),
	).Scan(&alreadyGuessed)

	if alreadyGuessed {
		c.JSON(http.StatusConflict, gin.H{"error": "you already tried that word"})
		return
	}

	if match.GameMode == models.GameModeHard {
		rows, err := s.db.Query(c.Request.Context(),
			`SELECT id, match_id, player_id, word_index, guess, result, guessed_at
			 FROM guesses
			 WHERE match_id=$1 AND player_id=$2 AND word_index=$3
			 ORDER BY guessed_at ASC`,
			matchID, userID, wordIdx,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load previous guesses"})
			return
		}
		defer rows.Close()

		previousGuesses := make([]models.Guess, 0)
		for rows.Next() {
			var previousGuess models.Guess
			var rawResult []byte
			if err := rows.Scan(
				&previousGuess.ID,
				&previousGuess.MatchID,
				&previousGuess.PlayerID,
				&previousGuess.WordIndex,
				&previousGuess.Guess,
				&rawResult,
				&previousGuess.GuessedAt,
			); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to scan previous guesses"})
				return
			}
			if err := json.Unmarshal(rawResult, &previousGuess.Result); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to parse previous guesses"})
				return
			}
			previousGuesses = append(previousGuesses, previousGuess)
		}

		if err := validateHardMode(req.Guess, previousGuesses); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
	}

	currentWord := s.wordSvc.GetWordAtIndex(c.Request.Context(), wordIdx)

	var guessCountOnWord int
	s.db.QueryRow(c.Request.Context(),
		`SELECT COUNT(*) FROM guesses
		 WHERE match_id=$1 AND player_id=$2 AND word_index=$3`,
		matchID, userID, wordIdx,
	).Scan(&guessCountOnWord)

	result := scoreGuess(req.Guess, currentWord)
	guess := models.Guess{
		ID:        uuid.New(),
		MatchID:   matchID,
		PlayerID:  userID,
		WordIndex: wordIdx,
		Guess:     req.Guess,
		Result:    result,
		GuessedAt: time.Now(),
	}

	resultJSON, _ := json.Marshal(result)
	s.db.Exec(c.Request.Context(),
		`INSERT INTO guesses (id, match_id, player_id, word_index, guess, result, guessed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		guess.ID, guess.MatchID, guess.PlayerID, guess.WordIndex,
		guess.Guess, resultJSON, guess.GuessedAt,
	)

	s.hub.SendToUserInMatch(matchID, opponentID, models.WSEvent{
		Type:    models.EventOpponentGuess,
		Payload: map[string]any{"result": result},
	})

	solved := req.Guess == currentWord
	correctTiles := 0
	for _, r := range result {
		if r == models.TileCorrect {
			correctTiles++
		}
	}

	if !solved && correctTiles > 0 {
		partialHeal := correctTiles * 2
		newPartialHP := min(myHP+partialHeal, models.StartingHP)
		var updatedAHP, updatedBHP int
		if isPlayerA {
			s.db.QueryRow(c.Request.Context(),
				`UPDATE matches SET player_a_hp=$1 WHERE id=$2 RETURNING player_a_hp, player_b_hp`,
				newPartialHP, matchID,
			).Scan(&updatedAHP, &updatedBHP)
		} else {
			s.db.QueryRow(c.Request.Context(),
				`UPDATE matches SET player_b_hp=$1 WHERE id=$2 RETURNING player_a_hp, player_b_hp`,
				newPartialHP, matchID,
			).Scan(&updatedAHP, &updatedBHP)
		}
		s.hub.SendToMatch(matchID, models.WSEvent{
			Type: models.EventHPUpdate,
			Payload: map[string]any{
				"player_a_hp": updatedAHP,
				"player_b_hp": updatedBHP,
			},
		})
		if isPlayerA {
			s.checkDeath(context.Background(), &match, updatedAHP, updatedBHP, userID, opponentID)
		} else {
			s.checkDeath(context.Background(), &match, updatedBHP, updatedAHP, userID, opponentID)
		}
	}
	if solved {
		newMyHP := min(myHP+models.SolveBonus, models.StartingHP)
		nextWordIdx := wordIdx + 1
		var updatedAHP, updatedBHP int

		if isPlayerA {
			s.db.QueryRow(c.Request.Context(),
				`UPDATE matches
				 SET player_a_hp=$1, player_a_word_idx=$2
				 WHERE id=$3
				 RETURNING player_a_hp, player_b_hp`,
				newMyHP, nextWordIdx, matchID,
			).Scan(&updatedAHP, &updatedBHP)
		} else {
			s.db.QueryRow(c.Request.Context(),
				`UPDATE matches
				 SET player_b_hp=$1, player_b_word_idx=$2
				 WHERE id=$3
				 RETURNING player_a_hp, player_b_hp`,
				newMyHP, nextWordIdx, matchID,
			).Scan(&updatedAHP, &updatedBHP)
		}

		nextWord := s.wordSvc.GetWordAtIndex(c.Request.Context(), nextWordIdx)

		s.hub.SendToMatch(matchID, models.WSEvent{
			Type: models.EventWordSolved,
			Payload: map[string]any{
				"player_id":        userID.String(),
				"next_word_index":  nextWordIdx,
				"next_word_length": len(nextWord),
			},
		})

		s.hub.SendToMatch(matchID, models.WSEvent{
			Type: models.EventHPUpdate,
			Payload: map[string]any{
				"player_a_hp": updatedAHP,
				"player_b_hp": updatedBHP,
			},
		})

		if isPlayerA {
			if updatedBHP <= 0 {
				go s.resolveMatch(context.Background(), &match, &userID)
			}
			s.checkDeath(context.Background(), &match, updatedAHP, updatedBHP, userID, opponentID)
		} else {
			if updatedAHP <= 0 {
				go s.resolveMatch(context.Background(), &match, &userID)
			}
			s.checkDeath(context.Background(), &match, updatedBHP, updatedAHP, userID, opponentID)
		}

	} else if guessCountOnWord+1 >= maxGuesses {
		newMyHP := myHP - models.FailPenalty
		var updatedAHP, updatedBHP int
		if isPlayerA {
			s.db.QueryRow(c.Request.Context(),
				`UPDATE matches SET player_a_hp=$1 WHERE id=$2 RETURNING player_a_hp, player_b_hp`,
				newMyHP, matchID,
			).Scan(&updatedAHP, &updatedBHP)
		} else {
			s.db.QueryRow(c.Request.Context(),
				`UPDATE matches SET player_b_hp=$1 WHERE id=$2 RETURNING player_a_hp, player_b_hp`,
				newMyHP, matchID,
			).Scan(&updatedAHP, &updatedBHP)
		}

		currentMyHP := updatedAHP
		if !isPlayerA {
			currentMyHP = updatedBHP
		}

		nextWordIdx := wordIdx + 1
		if isPlayerA {
			s.db.Exec(c.Request.Context(),
				`UPDATE matches SET player_a_word_idx=$1 WHERE id=$2`, nextWordIdx, matchID)
		} else {
			s.db.Exec(c.Request.Context(),
				`UPDATE matches SET player_b_word_idx=$1 WHERE id=$2`, nextWordIdx, matchID)
		}

		nextWord := s.wordSvc.GetWordAtIndex(c.Request.Context(), nextWordIdx)
		s.hub.SendToUser(userID, models.WSEvent{
			Type: "guess_reset",
			Payload: map[string]any{
				"reset":            true,
				"my_hp":            newMyHP,
				"next_word_index":  nextWordIdx,
				"next_word_length": len(nextWord),
			},
		})

		s.hub.SendToUserInMatch(matchID, userID, models.WSEvent{
			Type:    models.EventHPUpdate,
			Payload: map[string]any{"my_hp": currentMyHP, "reset": true},
		})

		s.hub.SendToUser(userID, models.WSEvent{
			Type:    models.EventGuessReset,
			Payload: map[string]any{"reset": true, "my_hp": currentMyHP},
		})

		s.hub.SendToMatch(matchID, models.WSEvent{
			Type: models.EventWordSolved,
			Payload: map[string]any{
				"player_id": userID.String(),
				"reset":     true,
				"reason":    "max_guesses",
			},
		})

		s.hub.SendToMatch(matchID, models.WSEvent{
			Type: models.EventHPUpdate,
			Payload: map[string]any{
				"player_a_hp": updatedAHP,
				"player_b_hp": updatedBHP,
			},
		})

		if currentMyHP <= 0 {
			go s.resolveMatch(context.Background(), &match, &opponentID)
		}
		if isPlayerA {
			s.checkDeath(context.Background(), &match, updatedAHP, updatedBHP, userID, opponentID)
		} else {
			s.checkDeath(context.Background(), &match, updatedBHP, updatedAHP, userID, opponentID)
		}
	}

	c.JSON(http.StatusOK, guess)
}

func (s *Service) HandleForfeitMatch(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	matchID, err := uuid.Parse(c.Param("matchID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid match id"})
		return
	}

	var match models.Match
	err = s.db.QueryRow(c.Request.Context(),
		`SELECT id, player_a_id, player_b_id, status, winner_id, player_a_hp, player_b_hp,
		        player_a_word_idx, player_b_word_idx, started_at, finished_at
		 FROM matches WHERE id=$1`, matchID,
	).Scan(
		&match.ID, &match.PlayerAID, &match.PlayerBID, &match.Status, &match.WinnerID,
		&match.PlayerAHP, &match.PlayerBHP, &match.PlayerAWordIdx, &match.PlayerBWordIdx,
		&match.StartedAt, &match.FinishedAt,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "match not found"})
		return
	}
	if match.Status != models.MatchActive {
		c.JSON(http.StatusConflict, gin.H{"error": "match is not active"})
		return
	}

	var winnerID uuid.UUID
	var forfeiterHPUpdate map[string]any
	switch userID {
	case match.PlayerAID:
		winnerID = match.PlayerBID
		s.db.Exec(c.Request.Context(), `UPDATE matches SET player_a_hp=0 WHERE id=$1`, matchID)
		forfeiterHPUpdate = map[string]any{
			"player_a_hp": 0,
			"player_b_hp": match.PlayerBHP,
		}
	case match.PlayerBID:
		winnerID = match.PlayerAID
		s.db.Exec(c.Request.Context(), `UPDATE matches SET player_b_hp=0 WHERE id=$1`, matchID)
		forfeiterHPUpdate = map[string]any{
			"player_a_hp": match.PlayerAHP,
			"player_b_hp": 0,
		}
	default:
		c.JSON(http.StatusForbidden, gin.H{"error": "not a participant"})
		return
	}

	s.hub.SendToMatch(matchID, models.WSEvent{
		Type:    models.EventHPUpdate,
		Payload: forfeiterHPUpdate,
	})

	s.hub.SendToUser(winnerID, models.WSEvent{
		Type:    models.EventOpponentLeft,
		Payload: map[string]any{"match_id": matchID.String()},
	})

	s.resolveMatch(c.Request.Context(), &match, &winnerID)
	c.Status(http.StatusNoContent)
}

func (s *Service) resolveMatch(ctx context.Context, match *models.Match, winnerID *uuid.UUID) {
	result, err := s.db.Exec(ctx,
		`UPDATE matches SET status='finished', winner_id=$1, finished_at=$2 
         WHERE id=$3 AND status='active'`,
		winnerID, time.Now(), match.ID,
	)
	if err != nil {
		return
	}
	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		return
	}

	var isRanked bool
	s.db.QueryRow(ctx, `SELECT is_ranked FROM matches WHERE id=$1`, match.ID).Scan(&isRanked)

	if winnerID != nil && isRanked {
		loserID := match.PlayerBID
		if *winnerID == match.PlayerBID {
			loserID = match.PlayerAID
		}

		var eloW, eloL int
		s.db.QueryRow(ctx, `SELECT elo FROM users WHERE id=$1`, *winnerID).Scan(&eloW)
		s.db.QueryRow(ctx, `SELECT elo FROM users WHERE id=$1`, loserID).Scan(&eloL)

		newW, newL := elo.Calculate(eloW, eloL)
		s.db.Exec(ctx, `UPDATE users SET elo=$1 WHERE id=$2`, newW, *winnerID)
		s.db.Exec(ctx, `UPDATE users SET elo=$1 WHERE id=$2`, newL, loserID)

		s.hub.SendToMatch(match.ID, models.WSEvent{
			Type: models.EventMatchOver,
			Payload: map[string]any{
				"winner_id": winnerID.String(),
				"elo_delta": newW - eloW,
				"is_ranked": true,
			},
		})
	} else {
		s.hub.SendToMatch(match.ID, models.WSEvent{
			Type: models.EventMatchOver,
			Payload: map[string]any{
				"winner_id": func() any {
					if winnerID != nil {
						return winnerID.String()
					}
					return nil
				}(),
				"elo_delta": 0,
				"is_ranked": false,
			},
		})
	}
}

func (s *Service) tiebreaker(ctx context.Context, matchID, playerAID, playerBID uuid.UUID) *uuid.UUID {
	correctTiles := func(playerID uuid.UUID) int {
		rows, err := s.db.Query(ctx,
			`SELECT result FROM guesses WHERE match_id=$1 AND player_id=$2`,
			matchID, playerID,
		)
		if err != nil {
			return 0
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var result []models.TileResult
			if err := rows.Scan(&result); err != nil {
				continue
			}
			for _, r := range result {
				if r == models.TileCorrect {
					count++
				}
			}
		}
		return count
	}

	aScore := correctTiles(playerAID)
	bScore := correctTiles(playerBID)

	if aScore > bScore {
		return &playerAID
	} else if bScore > aScore {
		return &playerBID
	}
	return nil // Draw
}

func (s *Service) checkDeath(ctx context.Context, match *models.Match, myHP, opponentHP int, myID, opponentID uuid.UUID) {
	if myHP <= 0 && opponentHP <= 0 {
		winner := s.tiebreaker(ctx, match.ID, match.PlayerAID, match.PlayerBID)
		go s.resolveMatch(ctx, match, winner)
	} else if myHP <= 0 {
		go s.resolveMatch(ctx, match, &opponentID)
	} else if opponentHP <= 0 {
		go s.resolveMatch(ctx, match, &myID)
	}
}

func scoreGuess(guess, target string) []models.TileResult {
	result := make([]models.TileResult, 5)
	remaining := make(map[rune]int)

	for i, ch := range target {
		if rune(guess[i]) == ch {
			result[i] = models.TileCorrect
		} else {
			remaining[ch]++
		}
	}

	for i, ch := range guess {
		if result[i] == models.TileCorrect {
			continue
		}
		if remaining[ch] > 0 {
			result[i] = models.TilePresent
			remaining[ch]--
		} else {
			result[i] = models.TileAbsent
		}
	}

	return result
}

func validateHardMode(guess string, previousGuesses []models.Guess) error {
	for _, prev := range previousGuesses {
		for i, r := range prev.Result {
			letter := string(prev.Guess[i])
			switch r {
			case models.TileCorrect:
				if string(guess[i]) != letter {
					return fmt.Errorf("must use '%s' in position %d", strings.ToUpper(letter), i+1)
				}
			case models.TilePresent:
				if !strings.ContainsRune(guess, rune(letter[0])) {
					return fmt.Errorf("must include '%s' somewhere in the guess", strings.ToUpper(letter))
				}
			}
		}
	}
	return nil
}
