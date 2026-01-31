package main

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	ID       string
	Username string
	Type     string
	Score    int
	Conn     *websocket.Conn
}

type Room struct {
	Clients        map[string]*Client
	GameState      *GameState
	mu             sync.RWMutex
	CurrentDrawer  string
	RoundStartTime time.Time
}

type Player struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Type      string `json:"type"`
	Score     int    `json:"score"`
	IsDrawing bool   `json:"isDrawing"`
}

type Message struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type DrawData struct {
	ImageData string `json:"imageData"`
}

type ChatMessage struct {
	Username string `json:"username"`
	Message  string `json:"message"`
	IsSystem bool   `json:"isSystem"`
}

type GameState struct {
	IsActive       bool            `json:"isActive"`
	CurrentWord    string          `json:"-"` // Hidden from clients
	WordHint       string          `json:"wordHint"`
	CurrentDrawer  string          `json:"currentDrawer"`
	TimeRemaining  int             `json:"timeRemaining"`
	RoundNumber    int             `json:"roundNumber"`
	WordChoices    []string        `json:"wordChoices,omitempty"`
	PlayersGuessed map[string]bool `json:"-"`
}
