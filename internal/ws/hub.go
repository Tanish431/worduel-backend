package ws

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/Tanish431/worduel/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		allowed := os.Getenv("FRONTEND_ORIGIN")
		return origin == allowed || strings.Contains(origin, "localhost")
	},
}

type Client struct {
	hub     *Hub
	conn    *websocket.Conn
	send    chan []byte
	UserID  uuid.UUID
	MatchID uuid.UUID
}

type Hub struct {
	mu         sync.RWMutex
	rooms      map[uuid.UUID][]*Client
	register   chan *Client
	unregister chan *Client
	broadcast  chan roomMsg
	jwtSecret  string
	db         *pgxpool.Pool
}

type Stats struct {
	ConnectedUsers int `json:"connected_users"`
	UsersInGame    int `json:"users_in_game"`
}

type roomMsg struct {
	matchID uuid.UUID
	data    []byte
}

func NewHub(jwtSecret string, db *pgxpool.Pool) *Hub {
	return &Hub{
		rooms:      make(map[uuid.UUID][]*Client),
		register:   make(chan *Client, 64),
		unregister: make(chan *Client, 64),
		broadcast:  make(chan roomMsg, 256),
		jwtSecret:  jwtSecret,
		db:         db,
	}
}

func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.rooms[c.MatchID] = append(h.rooms[c.MatchID], c)
			h.mu.Unlock()
		case c := <-h.unregister:
			h.mu.Lock()
			clients := h.rooms[c.MatchID]
			for i, cl := range clients {
				if cl == c {
					h.rooms[c.MatchID] = append(clients[:i], clients[i+1:]...)
					close(c.send)
					break
				}
			}
			h.mu.Unlock()
		case msg := <-h.broadcast:
			h.mu.RLock()
			for _, c := range h.rooms[msg.matchID] {
				select {
				case c.send <- msg.data:
				default:
					close(c.send)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) SendToMatch(matchID uuid.UUID, event models.WSEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("ws marshal error: %v", err)
		return
	}
	h.broadcast <- roomMsg{matchID: matchID, data: data}
}

func (h *Hub) SendToUser(userID uuid.UUID, event models.WSEvent) {
	data, _ := json.Marshal(event)
	h.mu.RLock()
	defer h.mu.RUnlock()
	log.Printf("SendToUser: looking for %s in %d rooms", userID, len(h.rooms))
	found := false
	for roomID, clients := range h.rooms {
		for _, c := range clients {
			log.Printf("  room %s has client %s", roomID, c.UserID)
			if c.UserID == userID {
				found = true
				select {
				case c.send <- data:
				default:
				}
			}
		}
	}
	log.Printf("SendToUser: found=%v for %s", found, userID)
}

func (h *Hub) SendToUserInMatch(matchID uuid.UUID, userID uuid.UUID, event models.WSEvent) {
	data, _ := json.Marshal(event)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.rooms[matchID] {
		if c.UserID != userID {
			continue
		}
		select {
		case c.send <- data:
		default:
		}
	}
}

func (h *Hub) HandleConnect(c *gin.Context) {
	tokenStr := c.Query("token")
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		return []byte(h.jwtSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	sub, ok := claims["sub"].(string)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	userID, err := uuid.Parse(sub)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	matchID, err := uuid.Parse(c.Query("match_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid match_id"})
		return
	}

	// Upgrade FIRST before any further checks
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	// Participant check AFTER upgrade — send close message on failure
	lobbyID := uuid.MustParse("00000000-0000-0000-0000-000000000000")
	if matchID != lobbyID {
		var count int
		h.db.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM matches WHERE id=$1 AND (player_a_id=$2 OR player_b_id=$2)`,
			matchID, userID,
		).Scan(&count)
		if count == 0 {
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(4001, "unauthorized"))
			conn.Close()
			return
		}
	}

	log.Printf("WS connected: userID=%s matchID=%s", userID, matchID)

	client := &Client{
		hub:     h,
		conn:    conn,
		send:    make(chan []byte, 256),
		UserID:  userID,
		MatchID: matchID,
	}

	h.register <- client

	// If connecting to a specific match (not lobby), send current match state
	if matchID != lobbyID {
		var playerAID, playerBID uuid.UUID
		var gameMode string
		err := h.db.QueryRow(context.Background(),
			`SELECT player_a_id, player_b_id, game_mode FROM matches WHERE id=$1`, matchID,
		).Scan(&playerAID, &playerBID, &gameMode)
		if err == nil {
			var opponentID uuid.UUID
			var opponentUsername string
			var isPlayerA bool
			if userID == playerAID {
				opponentID = playerBID
				isPlayerA = true
			} else {
				opponentID = playerAID
				isPlayerA = false
			}

			err = h.db.QueryRow(context.Background(),
				`SELECT username FROM users WHERE id=$1`, opponentID,
			).Scan(&opponentUsername)

			if err == nil {
				event := models.WSEvent{
					Type: models.EventMatchFound,
					Payload: map[string]any{
						"match_id":          matchID.String(),
						"opponent_id":       opponentID.String(),
						"opponent_username": opponentUsername,
						"is_player_a":       isPlayerA,
						"game_mode":         gameMode,
					},
				}
				data, _ := json.Marshal(event)
				client.send <- data
			}
		}
	}

	go client.writePump()
	go client.readPump()
}

func (h *Hub) Stats() Stats {
	h.mu.RLock()
	defer h.mu.RUnlock()

	lobbyID := uuid.MustParse("00000000-0000-0000-0000-000000000000")
	uniqueUsers := make(map[uuid.UUID]bool)
	inGameUsers := make(map[uuid.UUID]bool)

	for roomID, clients := range h.rooms {
		for _, c := range clients {
			uniqueUsers[c.UserID] = true
			if roomID != lobbyID {
				inGameUsers[c.UserID] = true
			}
		}
	}

	return Stats{
		ConnectedUsers: len(uniqueUsers),
		UsersInGame:    len(inGameUsers),
	}
}

func (c *Client) writePump() {
	defer c.conn.Close()
	for data := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
			break
		}
	}
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}
