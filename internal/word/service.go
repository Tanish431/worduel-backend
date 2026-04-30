package word

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db         *pgxpool.Pool
	validWords map[string]bool
}

func NewService(db *pgxpool.Pool) *Service {
	s := &Service{
		db:         db,
		validWords: make(map[string]bool),
	}
	s.loadWordCache()
	return s
}

func (s *Service) loadWordCache() {
	rows, err := s.db.Query(context.Background(),
		`SELECT word FROM word_list WHERE is_valid = true`,
	)
	if err != nil {
		log.Printf("load word cache: %v", err)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var word string
		if err := rows.Scan(&word); err != nil {
			continue
		}
		s.validWords[strings.ToLower(word)] = true
		count++
	}
	log.Printf("word cache loaded: %d words", count)
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
	return s.validWords[strings.ToLower(guess)]
}

func (s *Service) TotalAnswerWords(ctx context.Context) int {
	var count int
	s.db.QueryRow(ctx, `SELECT COUNT(*) FROM word_list WHERE is_answer = true`).Scan(&count)
	return count
}
