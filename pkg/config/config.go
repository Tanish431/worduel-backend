package config

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Config struct {
	Port               string
	DatabaseURL        string
	RedisUrl           string
	JWTSecret          string
	FrontendOrigin     string
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
}

func Load() *Config {
	return &Config{
		Port:               getEnv("PORT", "8080"),
		DatabaseURL:        getEnv("DATABASE_URL", "postgres://worduel:worduel@localhost:5432/worduel"),
		RedisUrl:           getEnv("REDIS_URL", "redis://localhost:6379"),
		JWTSecret:          getEnv("JWT_SECRET", "change-me-in-prod"),
		FrontendOrigin:     getEnv("FRONTEND_ORIGIN", "http://localhost:5173"),
		GoogleClientID:     getEnv("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: getEnv("GOOGLE_CLIENT_SECRET", ""),
		GoogleRedirectURL:  getEnv("GOOGLE_REDIRECT_URL", "http://localhost:8080/api/auth/google/callback"),
	}
}

func (c *Config) MustConnectDB() *pgxpool.Pool {
	config, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		log.Fatalf("parse db config: %v", err)
	}
	config.ConnConfig.ConnectTimeout = 10 * time.Second
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	return pool
}

func (c *Config) MustConnectRedis() *redis.Client {
	opts, err := redis.ParseURL(c.RedisUrl)
	if err != nil {
		log.Fatalf("parse redis url: %v", err)
	}
	opts.DialTimeout = 10 * time.Second
	opts.ReadTimeout = 10 * time.Second
	opts.WriteTimeout = 10 * time.Second
	return redis.NewClient(opts)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
