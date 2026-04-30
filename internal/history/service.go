package history

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Tanish431/worduel/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db *pgxpool.Pool
}

func NewService(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

type MatchHistoryEntry struct {
	MatchID          string     `json:"match_id"`
	OpponentID       string     `json:"opponent_id"`
	OpponentUsername string     `json:"opponent_username"`
	Result           string     `json:"result"`
	EloDelta         int        `json:"elo_delta"`
	GameMode         string     `json:"game_mode"`
	IsRanked         bool       `json:"is_ranked"`
	WordCount        int        `json:"word_count"`
	FinishedAt       *time.Time `json:"finished_at"`
}

type HistoryResponse struct {
	Matches []MatchHistoryEntry `json:"matches"`
	Total   int                 `json:"total"`
	Offset  int                 `json:"offset"`
	Limit   int                 `json:"limit"`
}

func (s *Service) HandleGetHistory(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	limit := 10
	offset := 0
	if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if o := c.Query("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}
	result := c.Query("result")
	gameMode := c.Query("game_mode")
	isRanked := c.Query("is_ranked")
	opponent := c.Query("opponent")
	dateRange := c.Query("date_range")

	query := `
		SELECT
			m.id,
			m.winner_id,
			m.player_a_id,
			m.player_b_id,
			m.game_mode,
			m.is_ranked,
			m.finished_at,
			CASE WHEN m.player_a_id = $1 THEN ub.username ELSE ua.username END AS opponent_username,
			CASE WHEN m.player_a_id = $1 THEN m.player_b_id ELSE m.player_a_id END AS opponent_id,
			COUNT(DISTINCT g.word_index) AS word_count
		FROM matches m
		JOIN users ua ON ua.id = m.player_a_id
		JOIN users ub ON ub.id = m.player_b_id
		LEFT JOIN guesses g ON g.match_id = m.id AND g.player_id = $1
		WHERE (m.player_a_id = $1 OR m.player_b_id = $1)
		AND m.status = 'finished'
	`
	args := []any{userID}
	argIdx := 2

	if gameMode != "" {
		query += ` AND m.game_mode = $` + strconv.Itoa(argIdx)
		args = append(args, gameMode)
		argIdx++
	}

	if isRanked != "" {
		query += ` AND m.is_ranked = $` + strconv.Itoa(argIdx)
		args = append(args, isRanked == "true")
		argIdx++
	}

	if opponent != "" {
		query += ` AND (
		(m.player_a_id = $1 AND ub.username ILIKE $` + strconv.Itoa(argIdx) + `) OR
		(m.player_b_id = $1 AND ua.username ILIKE $` + strconv.Itoa(argIdx) + `))`
		args = append(args, "%"+opponent+"%")
		argIdx++
	}

	if dateRange == "week" {
		query += ` AND m.finished_at >= NOW() - INTERVAL '7 days'`
	} else if dateRange == "month" {
		query += ` AND m.finished_at >= NOW() - INTERVAL '30 days'`
	}

	query += `GROUP BY m.id, ua.username, ub.username`

	if result == "win" {
		query += ` HAVING m.winner_id = $1`
	} else if result == "loss" {
		query += ` HAVING m.winner_id != $1 AND m.winner_id IS NOT NULL`
	} else if result == "draw" {
		query += ` HAVING m.winner_id IS NULL`
	}

	countQuery := ` SELECT COUNT(*) FROM (` + query + `) AS sub`
	var total int
	s.db.QueryRow(c.Request.Context(), countQuery, args...).Scan(&total)

	query += ` ORDER BY m.finished_at DESC LIMIT $` + strconv.Itoa(argIdx) + ` OFFSET $` + strconv.Itoa(argIdx+1)
	args = append(args, limit, offset)

	rows, err := s.db.Query(c.Request.Context(), query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	defer rows.Close()

	entries := []MatchHistoryEntry{}
	for rows.Next() {
		var e MatchHistoryEntry
		var matchID uuid.UUID
		var winnerID *uuid.UUID
		var playerAID, playerBID, opponentID uuid.UUID
		var wordCount int

		err := rows.Scan(
			&matchID, &winnerID, &playerAID, &playerBID,
			&e.GameMode, &e.IsRanked, &e.FinishedAt,
			&e.OpponentUsername, &opponentID, &wordCount,
		)
		if err != nil {
			continue
		}
		e.MatchID = matchID.String()
		e.OpponentID = opponentID.String()
		e.WordCount = wordCount

		if winnerID == nil {
			e.Result = "draw"
		} else if *winnerID == userID {
			e.Result = "win"
		} else {
			e.Result = "loss"
		}

		if e.IsRanked {
			// TODO: store elo snapshots for accurate delta — for now skip
			e.EloDelta = 0
		}
		entries = append(entries, e)
	}
	c.JSON(http.StatusOK, HistoryResponse{
		Matches: entries,
		Total:   total,
		Offset:  offset,
		Limit:   limit,
	})
}
