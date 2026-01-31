package main

import (
	"log"
	"time"
)

func endRound(room *Room) {
	room.mu.Lock()
	wordToReveal := room.GameState.CurrentWord
	room.GameState.IsActive = false
	room.mu.Unlock()

	broadcastChatMessage(room, ChatMessage{
		Username: "System",
		Message:  "The word was: " + wordToReveal,
		IsSystem: true,
	})

	broadcastGameState(room)

	// Start new round after delay
	go func() {
		time.Sleep(5 * time.Second)
		room.mu.Lock()
		if len(room.Clients) >= 2 {
			log.Println("🔄 Auto-starting next round...")
			startNewRound(room)
		} else {
			log.Println("⏸️ Not enough players for next round")
		}
		room.mu.Unlock()
	}()
}
