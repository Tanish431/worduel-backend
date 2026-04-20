package leaderboard

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db *pgxpool.Pool
}

func NewService(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

type Entry struct {
	Rank     int     `json:"rank"`
	Username string  `json:"username"`
	ELO      int     `json:"elo"`
	Wins     int     `json:"wins"`
	Losses   int     `json:"losses"`
	WinRate  float64 `json:"win_rate"`
}

func (s *Service) HandleGetLeaderboard(c *gin.Context) {
	rows, err := s.db.Query(c.Request.Context(), `
		SELECT
			u.username,
			u.elo,
			COUNT(m.id) FILTER (WHERE m.winner_id = u.id) AS wins,
			COUNT(m.id) FILTER (WHERE m.status='finished' AND m.winner_id != u.id)
		FROM users u
		LEFT JOIN matches m ON (m.player_a_id = u.id OR m.player_b_id = u.id)
		GROUP BY u.id
		ORDER BY u.elo DESC
		LIMIT 50
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	defer rows.Close()

	entries := []Entry{}
	rank := 1

	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Username, &e.ELO, &e.Wins, &e.Losses); err != nil {
			continue
		}
		e.Rank = rank
		total := e.Wins + e.Losses
		if total > 0 {
			e.WinRate = float64(e.Wins) / float64(total)
		}
		entries = append(entries, e)
		rank++
	}

	c.JSON(http.StatusOK, entries)
}
