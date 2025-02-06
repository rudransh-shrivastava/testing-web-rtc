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
			return true
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

	id := generateID()
	connection := &Connection{
		ID:   id,
		Conn: conn,
	}
	fmt.Println("New connection from id: ", id)
	connections.Lock()
	connections.m[id] = connection
	connections.Unlock()

	conn.WriteJSON(Message{
		Type:    "id",
		From:    "server",
		Payload: json.RawMessage(`"` + id + `"`),
	})

	fmt.Println("Sending peer list to it ")
	sendPeerList(connection)

	go handleMessages(connection)
}

func handleMessages(conn *Connection) {
	fmt.Println("now handling messages")
	defer func() {
		connections.Lock()
		fmt.Println("connection lost, deleting ID", conn.ID)
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

		switch msg.Type {
		case "offer", "answer", "ice-candidate":
			log.Println("Received this message from connection ID : MESSAGE ", conn.ID, msg)
			connections.RLock()
			if peer, ok := connections.m[msg.To]; ok {
				msg.From = conn.ID
				fmt.Println("writing this message back to connection ", peer.Conn.RemoteAddr().String(), msg)
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

	fmt.Println("sent peer list: ", peers)
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
	log.Printf("Starting server on :42069")
	log.Fatal(http.ListenAndServe(":42069", nil))
}
