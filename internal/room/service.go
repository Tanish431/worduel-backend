package room

import (
	"context"
	"math/rand"
	"net/http"
	"time"

	"github.com/Tanish431/worduel/internal/matchmaking"
	"github.com/Tanish431/worduel/internal/middleware"
	"github.com/Tanish431/worduel/internal/word"
	"github.com/Tanish431/worduel/internal/ws"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db      *pgxpool.Pool
	hub     *ws.Hub
	wordSvc *word.Service
	mmSvc   *matchmaking.Service
}

func NewService(db *pgxpool.Pool, hub *ws.Hub, wordSvc *word.Service, mmSvc *matchmaking.Service) *Service {
	return &Service{db: db, hub: hub, wordSvc: wordSvc, mmSvc: mmSvc}
}

func (s *Service) HandleCreateRoom(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	code := generateCode()

	_, err := s.db.Exec(c.Request.Context(),
		`INSERT INTO rooms (id, code, host_id, status, created_at)
		VALUES ($1, $2, $3, 'waiting', $4)`, uuid.New(), code, userID, time.Now(),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	go s.expireRoom(code)
	c.JSON(http.StatusCreated, gin.H{"code": code})
}

func (s *Service) HandleJoinRoom(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	code := c.Param("code")

	var roomID uuid.UUID
	var hostID uuid.UUID
	var status string
	err := s.db.QueryRow(c.Request.Context(),
		`SELECT id, host_id, status FROM rooms WHERE code = $1`, code,
	).Scan(&roomID, &hostID, &status)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}

	if status != "waiting" {
		c.JSON(http.StatusConflict, gin.H{"error": "room already matched"})
		return
	}

	if hostID == userID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot join your own room"})
		return
	}

	_, err = s.db.Exec(c.Request.Context(),
		`UPDATE rooms SET status='matched' WHERE id=$1`, roomID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	matchID, err := s.mmSvc.CreateMatchDirect(context.Background(), hostID, userID, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create match"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"match_id": matchID})
}

func (s *Service) expireRoom(code string) {
	time.Sleep(5 * time.Minute)
	s.db.Exec(context.Background(),
		`DELETE FROM rooms WHERE code=$1 AND status='waiting'`, code,
	)
}

func generateCode() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
