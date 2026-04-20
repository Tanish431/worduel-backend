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

	insertWords(conn, "seed/answers.txt", true)
	insertWords(conn, "seed/valid.txt", false)
	fmt.Println("seed done")
}

func insertWords(conn *pgx.Conn, path string, isAnswer bool) {
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		word := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if len(word) != 5 {
			continue
		}
		_, err := conn.Exec(context.Background(),
			`INSERT INTO word_list (word, is_answer, is_valid)
			 VALUES ($1, $2, true)
			 ON CONFLICT (word) DO UPDATE SET is_answer = EXCLUDED.is_answer OR word_list.is_answer`,
			word, isAnswer,
		)
		if err != nil {
			log.Printf("insert %s: %v", word, err)
			continue
		}
		count++
	}
	fmt.Printf("inserted %d words from %s\n", count, path)
}
