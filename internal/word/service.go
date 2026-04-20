package word

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db *pgxpool.Pool
}

func NewService(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

func (s *Service) GetWordAtIndex(ctx context.Context, index int) string {
	var w string
	err := s.db.QueryRow(ctx,
		`SELECT word FROM word_list
		WHERE is_answer = true
		ORDER BY id
		LIMIT 1 OFFSET $1`,
		index,
	).Scan(&w)
	if err != nil {
		log.Printf("get word at index %d: %v", index, err)
		return "crane"
	}
	return w
}

func (s *Service) IsValidGuess(ctx context.Context, guess string) bool {
	var exists bool
	s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM word_list WHERE word=$1 AND is_valid=true)`,
		strings.ToLower(guess),
	).Scan(&exists)
	return exists
}

func (s *Service) TotalAnswerWords(ctx context.Context) int {
	var count int
	s.db.QueryRow(ctx, `SELECT COUNT(*) FROM word_list WHERE is_answer = true`).Scan(&count)
	return count
}
