package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://worduel:worduel@localhost:5432/worduel"
	}

	conn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())

	answers := readWords("seed/answers.txt")
	validExtras := readWords("seed/valid.txt")

	if err := syncWords(conn, answers, validExtras); err != nil {
		log.Fatalf("sync words: %v", err)
	}
	fmt.Println("seed done")
}

func readWords(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	seen := make(map[string]struct{})
	words := make([]string, 0)
	for scanner.Scan() {
		word := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if len(word) != 5 {
			continue
		}
		if _, ok := seen[word]; ok {
			continue
		}
		seen[word] = struct{}{}
		words = append(words, word)
	}
	if err := scanner.Err(); err != nil {
		log.Fatalf("scan %s: %v", path, err)
	}
	fmt.Printf("loaded %d words from %s\n", len(words), path)
	return words
}

func syncWords(conn *pgx.Conn, answers, validExtras []string) error {
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE word_list SET is_answer = false, is_valid = false, answer_index = NULL`); err != nil {
		return err
	}

	for i, word := range answers {
		if _, err := tx.Exec(ctx,
			`INSERT INTO word_list (word, is_answer, is_valid, answer_index)
			 VALUES ($1, true, true, $2)
			 ON CONFLICT (word) DO UPDATE
			 SET is_answer = EXCLUDED.is_answer,
			     is_valid = EXCLUDED.is_valid,
			     answer_index = EXCLUDED.answer_index`,
			word, i+1,
		); err != nil {
			return err
		}
	}

	seen := make(map[string]struct{}, len(answers))
	for _, word := range answers {
		seen[word] = struct{}{}
	}

	insertedValid := 0
	for _, word := range validExtras {
		if _, ok := seen[word]; ok {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO word_list (word, is_answer, is_valid, answer_index)
			 VALUES ($1, false, true, NULL)
			 ON CONFLICT (word) DO UPDATE
			 SET is_answer = EXCLUDED.is_answer,
			     is_valid = EXCLUDED.is_valid,
			     answer_index = EXCLUDED.answer_index`,
			word,
		); err != nil {
			return err
		}
		insertedValid++
	}

	if _, err := tx.Exec(ctx, `DELETE FROM word_list WHERE is_answer = false AND is_valid = false`); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	fmt.Printf("synced %d answer words and %d extra valid words\n", len(answers), insertedValid)
	return nil
}
