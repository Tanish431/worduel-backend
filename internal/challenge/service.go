package challenge

import (
	"context"
	"net/http"
	"time"

	"github.com/Tanish431/worduel/internal/matchmaking"
	"github.com/Tanish431/worduel/internal/middleware"
	"github.com/Tanish431/worduel/internal/models"
	"github.com/Tanish431/worduel/internal/ws"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const challengeTTL = 15 * time.Second

type Service struct {
	db    *pgxpool.Pool
	rdb   *redis.Client
	hub   *ws.Hub
	mmSvc *matchmaking.Service
}

func NewService(db *pgxpool.Pool, rdb *redis.Client, hub *ws.Hub, mmSvc *matchmaking.Service) *Service {
	return &Service{db: db, rdb: rdb, hub: hub, mmSvc: mmSvc}
}

func (s *Service) HandleChallenge(c *gin.Context) {
	challengerID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	username := c.Param("username")

	var targetID uuid.UUID
	var targetUsername string

	err := s.db.QueryRow(c.Request.Context(),
		`SELECT id, username FROM users WHERE username=$1`, username,
	).Scan(&targetID, &targetUsername)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if targetID == challengerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot challenge yourself"})
		return
	}

	var challengerUsername string
	s.db.QueryRow(c.Request.Context(),
		`SELECT username FROM users WHERE id=$1`, challengerID,
	).Scan(&challengerUsername)

	// Store challenge in Redis with TTL
	key := "challenge:" + challengerID.String() + ":" + targetID.String()
	s.rdb.Set(c.Request.Context(), key, challengerID.String(), challengeTTL)

	s.hub.SendToUser(targetID, models.WSEvent{
		Type: models.EventChallengeRequest,
		Payload: map[string]any{
			"challenger_id":       challengerID.String(),
			"challenger_username": challengerUsername,
		},
	})

	c.JSON(http.StatusOK, gin.H{"message": "challenge sent"})
}

func (s *Service) HandleRespondChallenge(c *gin.Context) {
	targetID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		ChallengerID string `json:"challenger_id" binding:"required"`
		Accept       bool   `json:"accept"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	challengerID, err := uuid.Parse(req.ChallengerID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid challenger_id"})
		return
	}

	key := "challenge:" + challengerID.String() + ":" + targetID.String()
	exists, _ := s.rdb.Exists(c.Request.Context(), key).Result()
	if exists == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "challenge expired or not found"})
		return
	}
	s.rdb.Del(c.Request.Context(), key)

	if !req.Accept {
		s.hub.SendToUser(challengerID, models.WSEvent{
			Type:    models.EventChallengeDeclined,
			Payload: map[string]any{"message": "challenge declined"},
		})
		c.JSON(http.StatusOK, gin.H{"message": "challenge declined"})
		return
	}

	matchID, err := s.mmSvc.CreateMatchDirect(context.Background(), challengerID, targetID, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create match"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"match_id": matchID})
}

func (s *Service) HandleRematch(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	matchID, err := uuid.Parse(c.Param("matchID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid match ID"})
		return
	}

	var playerAID, playerBID uuid.UUID
	var isRanked bool

	err = s.db.QueryRow(c.Request.Context(),
		`SELECT player_a_id, player_b_id, is_ranked FROM matches WHERE id=$1`, matchID,
	).Scan(&playerAID, &playerBID, &isRanked)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "match not found"})
		return
	}
	if isRanked {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rematch only available for private matches"})
		return
	}

	opponentID := playerBID
	if userID == playerBID {
		opponentID = playerAID
	}

	key := "rematch:" + matchID.String() + ":" + userID.String()
	s.rdb.Set(c.Request.Context(), key, userID.String(), challengeTTL)

	s.hub.SendToUser(opponentID, models.WSEvent{
		Type: models.EventRematchRequest,
		Payload: map[string]any{
			"match_id":     matchID.String(),
			"requester_id": userID.String(),
		},
	})

	c.JSON(http.StatusOK, gin.H{"message": "rematch req sent"})
}

func (s *Service) HandleRespondRematch(c *gin.Context) {
	_, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"errror": "unauthorized"})
		return
	}

	matchID, err := uuid.Parse(c.Param("matchID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid match id"})
		return
	}
	var req struct {
		RequesterID string `json:"requester_id" binding:"required"`
		Accept      bool   `json:"accept"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	requesterID, err := uuid.Parse(req.RequesterID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid requester_id"})
		return
	}

	key := "rematch:" + matchID.String() + ":" + requesterID.String()
	exists, _ := s.rdb.Exists(c.Request.Context(), key).Result()
	if exists == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "rematch request expired or not found"})
		return
	}
	s.rdb.Del(c.Request.Context(), key)

	if !req.Accept {
		s.hub.SendToUser(requesterID, models.WSEvent{
			Type:    models.EventRematchDeclined,
			Payload: map[string]any{"message": "rematch declined"},
		})
		c.JSON(http.StatusOK, gin.H{"message": "rematch declined"})
		return
	}

	var playerAID, playerBID uuid.UUID
	s.db.QueryRow(c.Request.Context(),
		`SELECT player_a_id, player_b_id FROM matches WHERE id=$1`, matchID,
	).Scan(&playerAID, &playerBID)

	// Create new unranked match
	_, err = s.mmSvc.CreateMatchDirect(context.Background(), playerAID, playerBID, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create match"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "rematch started"})
}
