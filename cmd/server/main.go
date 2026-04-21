package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Tanish431/worduel/internal/auth"
	"github.com/Tanish431/worduel/internal/challenge"
	"github.com/Tanish431/worduel/internal/game"
	"github.com/Tanish431/worduel/internal/leaderboard"
	"github.com/Tanish431/worduel/internal/matchmaking"
	"github.com/Tanish431/worduel/internal/middleware"
	"github.com/Tanish431/worduel/internal/room"
	"github.com/Tanish431/worduel/internal/word"
	"github.com/Tanish431/worduel/internal/ws"
	"github.com/Tanish431/worduel/pkg/config"
	"github.com/gin-gonic/gin"
)

func main() {
	cfg := config.Load()

	db := cfg.MustConnectDB()
	rdb := cfg.MustConnectRedis()

	hub := ws.NewHub(cfg.JWTSecret)
	wordSvc := word.NewService(db)
	gameSvc := game.NewService(db, wordSvc, hub)
	authSvc := auth.NewService(db, cfg)
	mmSvc := matchmaking.NewService(db, rdb, hub, wordSvc, gameSvc)
	roomSvc := room.NewService(db, hub, wordSvc, mmSvc)
	lbSvc := leaderboard.NewService(db)
	challengeSvc := challenge.NewService(db, rdb, hub, mmSvc)

	go hub.Run()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mmSvc.RunQueue(ctx)

	r := gin.Default()

	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", cfg.FrontendOrigin)
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type")
		c.Header("Access-Control-Allow-Credentials", "true")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	api := r.Group("/api")
	{
		api.POST("/auth/register", authSvc.HandleRegister)
		api.POST("/auth/login", authSvc.HandleLogin)
		api.GET("/auth/google", authSvc.HandleGoogleLogin)
		api.GET("/auth/google/callback", authSvc.HandleGoogleCallback)
	}

	protected := r.Group("/api")
	protected.Use(middleware.Auth(cfg.JWTSecret))
	{
		protected.GET("/me", authSvc.HandleGetMe)
		protected.POST("/match/queue", mmSvc.HandleJoinQueue)
		protected.DELETE("/match/queue", mmSvc.HandleLeaveQueue)
		protected.GET("/match/:matchID", gameSvc.HandleGetMatch)
		protected.GET("/match/:matchID/summary", gameSvc.HandleGetMatchSummary)
		protected.POST("/match/:matchID/guess", gameSvc.HandleSubmitGuess)
		protected.POST("/room", roomSvc.HandleCreateRoom)
		protected.POST("/room/:code/join", roomSvc.HandleJoinRoom)
		protected.POST("/match/:matchID/forfeit", gameSvc.HandleForfeitMatch)
		protected.GET("/leaderboard", lbSvc.HandleGetLeaderboard)
		protected.POST("/challenge/:username", challengeSvc.HandleChallenge)
		protected.POST("/challenge/respond", challengeSvc.HandleRespondChallenge)
		protected.POST("/match/:matchID/rematch", challengeSvc.HandleRematch)
		protected.POST("/match/:matchID/rematch/respond", challengeSvc.HandleRespondRematch)
	}

	r.GET("/ws", hub.HandleConnect)

	// Graceful shutdown
	srv := &gin.Engine{}
	_ = srv

	log.Printf("server starting on :%s", cfg.Port)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := r.Run(":" + cfg.Port); err != nil {
			log.Fatalf("run: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = shutdownCtx

	cancel()
	log.Println("server stopped")
}
