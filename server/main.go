package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Connection struct {
	ID   string
	Conn *websocket.Conn
}

type Message struct {
	Type    string          `json:"type"`
	From    string          `json:"from"`
	To      string          `json:"to,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow all connections in this example
		},
	}
	connections = struct {
		sync.RWMutex
		m map[string]*Connection
	}{
		m: make(map[string]*Connection),
	}
)

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Upgrade error: %v", err)
		return
	}

	// Generate a random ID for this connection
	id := generateID()
	connection := &Connection{
		ID:   id,
		Conn: conn,
	}

	// Store the connection
	connections.Lock()
	connections.m[id] = connection
	connections.Unlock()

	// Send the client their ID
	conn.WriteJSON(Message{
		Type:    "id",
		From:    "server",
		Payload: json.RawMessage(`"` + id + `"`),
	})

	// Send list of available peers
	sendPeerList(connection)

	// Handle incoming messages
	go handleMessages(connection)
}

func handleMessages(conn *Connection) {
	defer func() {
		connections.Lock()
		delete(connections.m, conn.ID)
		connections.Unlock()
		conn.Conn.Close()
	}()

	for {
		var msg Message
		err := conn.Conn.ReadJSON(&msg)
		if err != nil {
			log.Printf("Read error: %v", err)
			return
		}

		// Handle message based on type
		switch msg.Type {
		case "offer", "answer", "ice-candidate":
			// Forward the message to the specified peer
			connections.RLock()
			if peer, ok := connections.m[msg.To]; ok {
				msg.From = conn.ID
				peer.Conn.WriteJSON(msg)
			}
			connections.RUnlock()
		}
	}
}

func sendPeerList(conn *Connection) {
	peers := []string{}
	connections.RLock()
	for id := range connections.m {
		if id != conn.ID {
			peers = append(peers, id)
		}
	}
	connections.RUnlock()

	conn.Conn.WriteJSON(Message{
		Type:    "peers",
		From:    "server",
		Payload: json.RawMessage(`["` + strings.Join(peers, `","`) + `"]`),
	})
}

func generateID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func main() {
	http.HandleFunc("/ws", handleWebSocket)
	log.Printf("Starting server on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
