package matchmaking

import (
	"context"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/Tanish431/worduel/internal/game"
	"github.com/Tanish431/worduel/internal/middleware"
	"github.com/Tanish431/worduel/internal/models"
	"github.com/Tanish431/worduel/internal/word"
	"github.com/Tanish431/worduel/internal/ws"
	"github.com/Tanish431/worduel/pkg/elo"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	queueKey          = "matchmaking:queue"
	guestQueueKey     = "matchmaking:guest_queue"
	easyQueueKey      = "matchmaking:easy_queue"
	hardQueueKey      = "matchmaking:hard_queue"
	guestEasyQueueKey = "matchmaking:guest_easy_queue"
	guestHardQueueKey = "matchmaking:guest_hard_queue"
	initialELORange   = 200
	queueInterval     = time.Second
)

type Service struct {
	db      *pgxpool.Pool
	rdb     *redis.Client
	hub     *ws.Hub
	wordSvc *word.Service
	gameSvc *game.Service
}

func NewService(db *pgxpool.Pool, rdb *redis.Client, hub *ws.Hub, wordSvc *word.Service, gameSvc *game.Service) *Service {
	return &Service{db: db, rdb: rdb, hub: hub, wordSvc: wordSvc, gameSvc: gameSvc}
}

type joinQueueRequest struct {
	GameMode string `json:"game_mode" binding:"required,oneof=easy hard"`
}

func (s *Service) HandleJoinQueue(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req joinQueueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req.GameMode = models.GameModeEasy
	}

	var eloRating int
	var isGuest bool

	err := s.db.QueryRow(c.Request.Context(),
		`SELECT elo, is_guest FROM users WHERE id=$1`, userID,
	).Scan(&eloRating, &isGuest)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	key := easyQueueKey
	if req.GameMode == models.GameModeHard && !isGuest {
		key = hardQueueKey
	} else if isGuest && req.GameMode == models.GameModeHard {
		key = guestHardQueueKey
	} else if isGuest {
		key = guestEasyQueueKey
	}

	err = s.rdb.ZAdd(c.Request.Context(), key, redis.Z{
		Score:  float64(eloRating),
		Member: userID.String(),
	}).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.Status(http.StatusNoContent)
}

func (s *Service) HandleLeaveQueue(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	s.rdb.ZRem(c.Request.Context(), queueKey, userID.String())
	s.rdb.ZRem(c.Request.Context(), guestQueueKey, userID.String())
	s.rdb.ZRem(c.Request.Context(), easyQueueKey, userID.String())
	s.rdb.ZRem(c.Request.Context(), hardQueueKey, userID.String())
	s.rdb.ZRem(c.Request.Context(), guestEasyQueueKey, userID.String())
	s.rdb.ZRem(c.Request.Context(), guestHardQueueKey, userID.String())
	c.Status(http.StatusNoContent)
}

func (s *Service) RunQueue(ctx context.Context) {
	ticker := time.NewTicker(queueInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.matchPlayers(ctx, easyQueueKey, true, models.GameModeEasy)
			s.matchPlayers(ctx, hardQueueKey, true, models.GameModeHard)
			s.matchPlayers(ctx, guestEasyQueueKey, false, models.GameModeEasy)
			s.matchPlayers(ctx, guestHardQueueKey, false, models.GameModeHard)
		}
	}
}

func (s *Service) matchPlayers(ctx context.Context, key string, isRanked bool, gameMode string) {
	entries, err := s.rdb.ZRangeWithScores(ctx, key, 0, -1).Result()
	if err != nil || len(entries) < 2 {
		return
	}

	for i := 0; i < len(entries)-1; i++ {
		a := entries[i]
		b := entries[i+1]
		if !elo.WithinRange(int(a.Score), int(b.Score), initialELORange) {
			continue
		}

		playerAID, _ := uuid.Parse(a.Member.(string))
		playerBID, _ := uuid.Parse(b.Member.(string))

		removed, err := s.rdb.ZRem(ctx, key, a.Member, b.Member).Result()
		if err != nil || removed != 2 {
			continue
		}

		go s.createMatchWithRanked(ctx, playerAID, playerBID, isRanked, gameMode)
		i++
	}
}

func (s *Service) createMatchWithRanked(ctx context.Context, playerAID, playerBID uuid.UUID, isRanked bool, gameMode string) {
	var usernameA, usernameB string
	s.db.QueryRow(ctx, `SELECT username FROM users WHERE id=$1`, playerAID).Scan(&usernameA)
	s.db.QueryRow(ctx, `SELECT username FROM users WHERE id=$1`, playerBID).Scan(&usernameB)

	matchID := uuid.New()
	startIdx := rand.IntN(1000)

	_, err := s.db.Exec(ctx,
		`INSERT INTO matches
		 (id, player_a_id, player_b_id, status, player_a_hp, player_b_hp,
		  player_a_word_idx, player_b_word_idx, is_ranked, game_mode, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		matchID, playerAID, playerBID, models.MatchActive,
		models.StartingHP, models.StartingHP, startIdx, startIdx, isRanked, gameMode, time.Now(),
	)
	if err != nil {
		return
	}

	s.hub.SendToUser(playerAID, models.WSEvent{
		Type: models.EventMatchFound,
		Payload: map[string]any{
			"match_id":          matchID.String(),
			"opponent_id":       playerBID.String(),
			"opponent_username": usernameB,
			"is_player_a":       true,
			"game_mode":         gameMode,
		},
	})
	s.hub.SendToUser(playerBID, models.WSEvent{
		Type: models.EventMatchFound,
		Payload: map[string]any{
			"match_id":          matchID.String(),
			"opponent_id":       playerAID.String(),
			"opponent_username": usernameA,
			"is_player_a":       false,
			"game_mode":         gameMode,
		},
	})

	go s.gameSvc.StartDrain(ctx, matchID)
}

func (s *Service) CreateMatchDirect(ctx context.Context, playerAID, playerBID uuid.UUID, isRanked bool, gameMode string) (uuid.UUID, error) {
	matchID := uuid.New()
	startIdx := rand.IntN(1000)

	_, err := s.db.Exec(ctx,
		`INSERT INTO matches
		(id, player_a_id, player_b_id, status, player_a_hp, player_b_hp,
		player_a_word_idx, player_b_word_idx, is_ranked, game_mode, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		matchID, playerAID, playerBID, models.MatchActive,
		models.StartingHP, models.StartingHP, startIdx, startIdx, isRanked, gameMode, time.Now(),
	)
	if err != nil {
		return uuid.UUID{}, err
	}

	var usernameA, usernameB string
	s.db.QueryRow(ctx, `SELECT username FROM users WHERE id=$1`, playerAID).Scan(&usernameA)
	s.db.QueryRow(ctx, `SELECT username FROM users WHERE id=$1`, playerBID).Scan(&usernameB)

	s.hub.SendToUser(playerAID, models.WSEvent{
		Type: models.EventMatchFound,
		Payload: map[string]any{
			"match_id":          matchID.String(),
			"opponent_id":       playerBID.String(),
			"opponent_username": usernameB,
			"is_player_a":       true,
			"game_mode":         gameMode,
		},
	})
	s.hub.SendToUser(playerBID, models.WSEvent{
		Type: models.EventMatchFound,
		Payload: map[string]any{
			"match_id":          matchID.String(),
			"opponent_id":       playerAID.String(),
			"opponent_username": usernameA,
			"is_player_a":       false,
			"game_mode":         gameMode,
		},
	})

	go s.gameSvc.StartDrain(context.Background(), matchID)

	return matchID, nil
}
