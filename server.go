package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for WebSocket
	},
}

var room = &Room{
	Clients:   make(map[string]*Client),
	GameState: &GameState{IsActive: false},
}

func wsHandler(c *gin.Context) {
	// Get username from query parameter
	username := c.Query("username")
	if username == "" {
		username = "Anonymous"
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Println("upgrade error:", err)
		return
	}
	defer conn.Close()

	// Create new client with UUID
	clientID := uuid.New().String()

	client := &Client{
		ID:       clientID,
		Conn:     conn,
		Username: username,
		Type:     "player",
		Score:    0,
	}

	// if no player is present then make this player the owner of room
	room.mu.Lock()
	if len(room.Clients) == 0 {
		log.Printf("👑 Client %s [%s] is the room owner\n", username, clientID)
		client.Type = "owner"
	}

	// Add client to room
	addClientToRoom(room, client)
	log.Printf("🔌 Client connected: %s [%s] (Total clients: %d)\n", username, clientID, len(room.Clients))
	room.mu.Unlock()

	// Send connection confirmation with client ID to the new client
	connMessage := Message{
		Type: "connected",
		Data: map[string]interface{}{
			"clientId": clientID,
			"username": username,
			"type":     client.Type,
		},
	}
	connJSON, _ := json.Marshal(connMessage)
	client.Conn.WriteMessage(websocket.TextMessage, connJSON)

	// Broadcast updated players list to all clients
	broadcastPlayers(room)

	// Send current game state to new player
	sendGameState(client)

	// Remove client from room on disconnect
	defer func() {
		room.mu.Lock()
		removeClientFromRoom(room, clientID)
		log.Printf("❌ Client disconnected: %s [%s] (Total clients: %d)\n", username, clientID, len(room.Clients))

		// Reset game if less than 2 players remain
		if len(room.Clients) < 2 && room.GameState.IsActive {
			log.Println("🔄 Less than 2 players remaining, resetting game...")
			room.GameState = &GameState{
				IsActive: false,
			}
			// Reset all scores
			for _, c := range room.Clients {
				c.Score = 0
			}

			room.GameState.PlayersGuessed = make(map[string]bool)

		}

		room.mu.Unlock()
		// Broadcast updated players list after disconnect
		broadcastPlayers(room)

		// Broadcast game state if it was reset
		if len(room.Clients) < 2 {
			broadcastGameState(room)
		}
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("read error: %v\n", err)
			return
		}

		var message Message
		err = json.Unmarshal(msg, &message)
		if err != nil {
			log.Printf("unmarshal error: %v\n", err)
			continue
		}

		handleMessage(client, message)
	}
}

func handleMessage(client *Client, message Message) {
	room.mu.Lock()

	// Flag to track mutex is unlocked
	unlocked := false
	defer func() {
		if !unlocked {
			room.mu.Unlock()
		}
	}()

	switch message.Type {
	case "draw":
		// Only allow current drawer to send draw data
		if room.GameState.IsActive && client.ID == room.GameState.CurrentDrawer {
			log.Printf("✏️ Draw data from %s, broadcasting to %d others\n", client.Username, len(room.Clients)-1)
			broadcastToOthers(room, client.ID, message)
		}

	case "chat":
		data, ok := message.Data.(map[string]interface{})
		if !ok {
			return
		}

		chatMsg, ok := data["message"].(string)
		if !ok || chatMsg == "" {
			return
		}

		// Check if message is correct guess
		if room.GameState.IsActive && client.ID != room.GameState.CurrentDrawer {
			if chatMsg == room.GameState.CurrentWord && room.GameState.PlayersGuessed[client.ID] != true {
				// Correct guess!
				client.Score += 100

				// Broadcast correct guess notification
				broadcastChatMessage(room, ChatMessage{
					Username: "System",
					Message:  client.Username + " guessed the word!",
					IsSystem: true,
				})

				// Update players list with new score
				broadcastPlayers(room)

				// Mark player as having guessed
				room.GameState.PlayersGuessed[client.ID] = true

				// check if all players have guessed the word then end if so

				allGuessed := true
				for _, c := range room.Clients {
					if c.ID != room.GameState.CurrentDrawer && !room.GameState.PlayersGuessed[c.ID] {
						allGuessed = false
						break
					}
				}

				// End round - must unlock before calling since endRound spawns goroutine
				room.mu.Unlock()
				unlocked = true

				if allGuessed {
					log.Println("🎉 All players have guessed the word, ending round early")
					endRound(room)
				}

				return
			}
		}

		// Broadcast regular chat message
		broadcastChatMessage(room, ChatMessage{
			Username: client.Username,
			Message:  chatMsg,
			IsSystem: false,
		})

	case "startGame":
		// Only owner can start the game and need at least 2 players
		log.Printf("🎮 Start game request from %s [%s] (Type: %s, IsActive: %v, Players: %d)\n", client.Username, client.ID, client.Type, room.GameState.IsActive, len(room.Clients))
		if client.Type == "owner" && !room.GameState.IsActive && len(room.Clients) >= 2 {
			log.Println("✅ Starting new round...")
			startNewRound(room)
		} else {
			if len(room.Clients) < 2 {
				log.Println("❌ Cannot start game - Need at least 2 players")
				broadcastChatMessage(room, ChatMessage{
					Username: "System",
					Message:  "Need at least 2 players to start the game!",
					IsSystem: true,
				})
			} else {
				log.Printf("❌ Cannot start game - Owner: %v, Active: %v\n", client.Type == "owner", room.GameState.IsActive)
			}
		}

	case "chooseWord":
		// Current drawer chooses word
		if client.ID == room.GameState.CurrentDrawer && len(room.GameState.WordChoices) > 0 {
			data, ok := message.Data.(map[string]interface{})
			if !ok {
				return
			}

			wordIndex, ok := data["wordIndex"].(float64)
			if !ok || int(wordIndex) >= len(room.GameState.WordChoices) {
				return
			}

			room.GameState.CurrentWord = room.GameState.WordChoices[int(wordIndex)]
			room.GameState.WordChoices = nil
			room.GameState.WordHint = generateHint(room.GameState.CurrentWord)
			room.RoundStartTime = time.Now()

			broadcastGameState(room)
			broadcastChatMessage(room, ChatMessage{
				Username: "System",
				Message:  client.Username + " is now drawing!",
				IsSystem: true,
			})

			// Start round timer
			go roundTimer(room)
		}
	}
}

func startNewRound(room *Room) {

	// if 10 rounds have been played, reset scores and send results
	if room.GameState != nil && room.GameState.RoundNumber >= 10 {
		log.Println("🏁 10 rounds completed, resetting scores and sending results")

		// Send final results
		results := []Player{}
		for _, c := range room.Clients {
			results = append(results, Player{
				ID:       c.ID,
				Username: c.Username,
				Type:     c.Type,
				Score:    c.Score,
			})
		}
		broadcastChatMessage(room, ChatMessage{
			Username: "System",
			Message:  "Final Results!",
			IsSystem: true,
		})

		resultMessage := Message{
			Type: "results",
			Data: results,
		}
		jsonData, _ := json.Marshal(resultMessage)
		for _, client := range room.Clients {
			client.Conn.WriteMessage(websocket.TextMessage, jsonData)
		}
		// Reset scores
		for _, c := range room.Clients {
			c.Score = 0
		}
		room.GameState.PlayersGuessed = make(map[string]bool)
	}

	// This function expects room.mu to already be locked
	log.Println("🎲 Starting new round...")

	// Get next drawer
	var drawerID string
	clientIDs := make([]string, 0, len(room.Clients))
	for id := range room.Clients {
		clientIDs = append(clientIDs, id)
	}

	if len(clientIDs) == 0 {
		log.Println("❌ No clients to start round")
		return
	}

	log.Printf("👥 Found %d clients\n", len(clientIDs))

	// Find current drawer index
	currentIndex := -1
	for i, id := range clientIDs {
		if id == room.CurrentDrawer {
			currentIndex = i
			break
		}
	}

	// Get next drawer
	nextIndex := (currentIndex + 1) % len(clientIDs)
	drawerID = clientIDs[nextIndex]
	room.CurrentDrawer = drawerID

	log.Printf("✏️ Next drawer: %s\n", drawerID)

	// Generate word choices
	wordChoices := getRandomWords(3)
	log.Printf("📝 Word choices: %v\n", wordChoices)

	// Preserve round number or start at 1
	currentRound := 0
	if room.GameState != nil {
		currentRound = room.GameState.RoundNumber
	}

	room.GameState = &GameState{
		IsActive:       true,
		CurrentDrawer:  drawerID,
		TimeRemaining:  80,
		RoundNumber:    currentRound + 1,
		WordChoices:    wordChoices,
		PlayersGuessed: make(map[string]bool),
	}

	log.Printf("🎮 Game state updated - Round %d\n", room.GameState.RoundNumber)

	broadcastGameState(room)
	broadcastPlayers(room)

	// Clear canvas for all players at start of new round
	clearMessage := Message{
		Type: "draw",
		Data: map[string]interface{}{
			"type": "clear",
		},
	}
	jsonData, _ := json.Marshal(clearMessage)
	for _, client := range room.Clients {
		client.Conn.WriteMessage(websocket.TextMessage, jsonData)
	}

	broadcastChatMessage(room, ChatMessage{
		Username: "System",
		Message:  "New round started! Waiting for drawer to choose a word...",
		IsSystem: true,
	})

	log.Println("✅ Round started successfully")
}

func roundTimer(room *Room) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		room.mu.Lock()

		if !room.GameState.IsActive || len(room.GameState.WordChoices) > 0 {
			room.mu.Unlock()
			return
		}

		elapsed := int(time.Since(room.RoundStartTime).Seconds())
		remaining := 80 - elapsed

		if remaining <= 0 {
			// Time's up!
			broadcastChatMessage(room, ChatMessage{
				Username: "System",
				Message:  "Time's up!",
				IsSystem: true,
			})
			room.mu.Unlock()
			endRound(room)
			return
		}

		room.GameState.TimeRemaining = remaining
		broadcastGameState(room)
		room.mu.Unlock()
	}
}

func addClientToRoom(room *Room, client *Client) {
	// mutex is already locked by caller function
	room.Clients[client.ID] = client
}

func removeClientFromRoom(room *Room, clientID string) {

	// if player is owner and there are other players, assign new owner
	if room.Clients[clientID].Type == "owner" && len(room.Clients) > 1 {
		for id, c := range room.Clients {
			if id != clientID {
				c.Type = "owner"
				log.Printf("👑 Client %s [%s] is the new room owner\n", c.Username, c.ID)
				break
			}
		}
	}

	delete(room.Clients, clientID)
}

func broadcastPlayers(room *Room) {
	// Build players list
	players := []Player{}
	for _, client := range room.Clients {
		players = append(players, Player{
			ID:        client.ID,
			Username:  client.Username,
			Type:      client.Type,
			Score:     client.Score,
			IsDrawing: room.GameState.IsActive && client.ID == room.GameState.CurrentDrawer,
		})
	}

	// Create message
	message := Message{
		Type: "players",
		Data: players,
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling players: %v\n", err)
		return
	}

	// Broadcast to all clients
	for _, client := range room.Clients {
		err := client.Conn.WriteMessage(websocket.TextMessage, jsonData)
		if err != nil {
			log.Printf("Error broadcasting to client %s: %v\n", client.ID, err)
		}
	}

	log.Printf("📤 Broadcasted players list to %d clients\n", len(room.Clients))
}

func broadcastGameState(room *Room) {
	// Check if game state exists
	if room.GameState == nil {
		return
	}

	for _, client := range room.Clients {
		// Create a copy of game state (dereference to copy the struct)
		stateCopy := *room.GameState

		// If this client is the drawer, show them the full word
		if client.ID == room.GameState.CurrentDrawer {
			stateCopy.WordHint = room.GameState.CurrentWord
		}

		message := Message{
			Type: "gameState",
			Data: &stateCopy,
		}

		jsonData, err := json.Marshal(message)
		if err != nil {
			log.Printf("Error marshaling game state: %v\n", err)
			continue
		}

		err = client.Conn.WriteMessage(websocket.TextMessage, jsonData)
		if err != nil {
			log.Printf("Error broadcasting game state to client %s: %v\n", client.ID, err)
		}
	}
}

func sendGameState(client *Client) {
	room.mu.RLock()
	defer room.mu.RUnlock()

	// Create a copy of game state
	stateCopy := room.GameState

	// Check if game state exists
	if room.GameState == nil {
		return
	}

	// If this client is the drawer, show them the full word
	if client.ID == room.GameState.CurrentDrawer {
		stateCopy.WordHint = room.GameState.CurrentWord
	}

	message := Message{
		Type: "gameState",
		Data: stateCopy,
	}

	jsonData, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling game state: %v\n", err)
		return
	}

	err = client.Conn.WriteMessage(websocket.TextMessage, jsonData)
	if err != nil {
		log.Printf("Error sending game state to client %s: %v\n", client.ID, err)
	}
}

func broadcastChatMessage(room *Room, chatMsg ChatMessage) {
	message := Message{
		Type: "chat",
		Data: chatMsg,
	}

	jsonData, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling chat message: %v\n", err)
		return
	}

	for _, client := range room.Clients {
		err := client.Conn.WriteMessage(websocket.TextMessage, jsonData)
		if err != nil {
			log.Printf("Error broadcasting chat to client %s: %v\n", client.ID, err)
		}
	}
}

func broadcastToOthers(room *Room, senderID string, message Message) {
	jsonData, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling message: %v\n", err)
		return
	}

	log.Printf("📦 Broadcasting message: %s\n", string(jsonData))

	for _, client := range room.Clients {
		if client.ID != senderID {
			log.Printf("  → Sending to client %s (%s)\n", client.Username, client.ID)
			err := client.Conn.WriteMessage(websocket.TextMessage, jsonData)
			if err != nil {
				log.Printf("Error broadcasting to client %s: %v\n", client.ID, err)
			}
		}
	}

	log.Println("✅ Broadcast complete")
}

func setupRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()

	router.Use(gin.LoggerWithWriter(os.Stdout))

	router.Use(gin.Recovery())

	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	// WebSocket route
	router.GET("/ws", wsHandler)

	return router
}

func main() {
	// Initialize random seed
	rand.Seed(time.Now().UnixNano())

	log.Println("🚀 Starting server on port 42069")

	router := setupRouter()

	if err := router.Run(":42069"); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}
