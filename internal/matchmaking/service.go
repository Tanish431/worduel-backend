package matchmaking

import (
	"context"
	"log"
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
	queueKey        = "matchmaking:queue"
	initialELORange = 200
	queueInterval   = time.Second
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

func (s *Service) HandleJoinQueue(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var eloRating int
	err := s.db.QueryRow(c.Request.Context(),
		`SELECT elo FROM users WHERE id=$1`, userID,
	).Scan(&eloRating)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	err = s.rdb.ZAdd(c.Request.Context(), queueKey, redis.Z{
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
			s.matchPlayers(ctx)
		}
	}
}

func (s *Service) matchPlayers(ctx context.Context) {
	entries, err := s.rdb.ZRangeWithScores(ctx, queueKey, 0, -1).Result()
	log.Printf("queue tick: %d players, err=%v", len(entries), err)
	if err != nil || len(entries) < 2 {
		return
	}

	for i := 0; i < len(entries)-1; i++ {
		a := entries[i]
		b := entries[i+1]
		log.Printf("checking pair: %v (%.0f) vs %v (%.0f)", a.Member, a.Score, b.Member, b.Score)
		if !elo.WithinRange(int(a.Score), int(b.Score), initialELORange) {
			log.Printf("elo range exceeded, skipping")
			continue
		}

		playerAID, _ := uuid.Parse(a.Member.(string))
		playerBID, _ := uuid.Parse(b.Member.(string))

		removed, err := s.rdb.ZRem(ctx, queueKey, a.Member, b.Member).Result()
		log.Printf("zrem result: removed=%d err=%v", removed, err)
		if err != nil || removed != 2 {
			continue
		}

		go s.createMatch(ctx, playerAID, playerBID)
		i++
	}
}

func (s *Service) createMatch(ctx context.Context, playerAID, playerBID uuid.UUID) {
	matchID := uuid.New()

	_, err := s.db.Exec(ctx,
		`INSERT INTO matches
		 (id, player_a_id, player_b_id, status, player_a_hp, player_b_hp,
		  player_a_word_idx, player_b_word_idx, is_ranked, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		matchID, playerAID, playerBID, models.MatchActive,
		models.StartingHP, models.StartingHP, 0, 0, true, time.Now(),
	)
	if err != nil {
		log.Printf("create match: %v", err)
		return
	}

	s.hub.SendToUser(playerAID, models.WSEvent{
		Type: models.EventMatchFound,
		Payload: map[string]any{
			"match_id":    matchID.String(),
			"opponent_id": playerBID.String(),
			"is_player_a": true,
		},
	})
	s.hub.SendToUser(playerBID, models.WSEvent{
		Type: models.EventMatchFound,
		Payload: map[string]any{
			"match_id":    matchID.String(),
			"opponent_id": playerAID.String(),
			"is_player_a": false,
		},
	})

	go s.gameSvc.StartDrain(ctx, matchID)
}

func (s *Service) CreateMatchDirect(ctx context.Context, playerAID, playerBID uuid.UUID, isRanked bool) (uuid.UUID, error) {
	matchID := uuid.New()
	startIdx := rand.IntN(1000)

	_, err := s.db.Exec(ctx,
		`INSERT INTO matches
		 (id, player_a_id, player_b_id, status, player_a_hp, player_b_hp,
		  player_a_word_idx, player_b_word_idx, is_ranked, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		matchID, playerAID, playerBID, models.MatchActive,
		models.StartingHP, models.StartingHP, startIdx, startIdx, isRanked, time.Now(),
	)
	if err != nil {
		return uuid.UUID{}, err
	}

	s.hub.SendToUser(playerAID, models.WSEvent{
		Type: models.EventMatchFound,
		Payload: map[string]any{
			"match_id":    matchID.String(),
			"opponent_id": playerBID.String(),
			"is_player_a": true,
		},
	})
	s.hub.SendToUser(playerBID, models.WSEvent{
		Type: models.EventMatchFound,
		Payload: map[string]any{
			"match_id":    matchID.String(),
			"opponent_id": playerAID.String(),
			"is_player_a": false,
		},
	})

	go s.gameSvc.StartDrain(context.Background(), matchID)

	return matchID, nil
}
