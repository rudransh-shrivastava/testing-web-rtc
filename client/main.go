package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
)

type Message struct {
	Type    string          `json:"type"`
	From    string          `json:"from"`
	To      string          `json:"to,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

var (
	myID           string
	signalingSrv   *websocket.Conn
	peerConnection *webrtc.PeerConnection
	dataChannel    *webrtc.DataChannel
	mut            sync.Mutex
	connectedPeer  string
)

func main() {
	serverIP := os.Args[1]
	dialAddr := fmt.Sprintf("ws://%s:42069/ws", serverIP)
	// Connect to signaling server
	var err error
	signalingSrv, _, err = websocket.DefaultDialer.Dial(dialAddr, nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer signalingSrv.Close()

	// Initialize WebRTC
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	peerConnection, err = webrtc.NewPeerConnection(config)
	if err != nil {
		log.Fatal(err)
	}
	defer peerConnection.Close()

	// Handle incoming data channels
	peerConnection.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Received data channel\n")
		dataChannel = dc
		setupDataChannel(dc)
	})

	// Handle ICE candidates
	peerConnection.OnICECandidate(func(ice *webrtc.ICECandidate) {
		if ice != nil {
			candidateJSON, err := json.Marshal(ice.ToJSON())
			if err != nil {
				return
			}
			signalingSrv.WriteJSON(Message{
				Type:    "ice-candidate",
				To:      connectedPeer,
				Payload: candidateJSON,
			})
		}
	})

	// Listen for messages from signaling server
	go handleSignalingMessages()

	// Handle user input
	fmt.Println("Type your messages (press Enter to send):")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()
		if dataChannel != nil {
			dataChannel.SendText(text)
			fmt.Printf("You: %s\n", text)
		} else {
			fmt.Println("Not connected to peer yet")
		}
	}
}

func handleSignalingMessages() {
	for {
		var msg Message
		err := signalingSrv.ReadJSON(&msg)
		if err != nil {
			log.Println("read:", err)
			return
		}

		switch msg.Type {
		case "id":
			var id string
			json.Unmarshal(msg.Payload, &id)
			myID = id
			log.Printf("Connected to server with ID: %s", myID)

		case "peers":
			var peers []string
			json.Unmarshal(msg.Payload, &peers)
			if len(peers) > 0 {
				// Only create offer if our ID is "greater" than peer's ID
				// This ensures only one side creates the offer
				peerID := peers[0]
				if myID > peerID {
					log.Printf("Creating offer for peer: %s", peerID)
					createOffer(peerID)
				} else {
					log.Printf("Waiting for offer from peer: %s", peerID)
				}
			}

		case "offer":
			var offer webrtc.SessionDescription
			json.Unmarshal(msg.Payload, &offer)
			handleOffer(msg.From, offer)

		case "answer":
			var answer webrtc.SessionDescription
			json.Unmarshal(msg.Payload, &answer)
			err := peerConnection.SetRemoteDescription(answer)
			if err != nil {
				log.Printf("Error setting remote description: %v", err)
			}

		case "ice-candidate":
			var candidate webrtc.ICECandidateInit
			json.Unmarshal(msg.Payload, &candidate)
			err := peerConnection.AddICECandidate(candidate)
			if err != nil {
				log.Printf("Error adding ICE candidate: %v", err)
			}
		}
	}
}

func createOffer(peerID string) {
	connectedPeer = peerID

	// Create data channel
	var err error
	dataChannel, err = peerConnection.CreateDataChannel("chat", nil)
	if err != nil {
		log.Fatal(err)
	}
	setupDataChannel(dataChannel)

	// Create offer
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		log.Fatal(err)
	}

	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		log.Fatal(err)
	}

	offerJSON, err := json.Marshal(offer)
	if err != nil {
		log.Fatal(err)
	}

	signalingSrv.WriteJSON(Message{
		Type:    "offer",
		To:      peerID,
		Payload: offerJSON,
	})
}

func handleOffer(peerID string, offer webrtc.SessionDescription) {
	connectedPeer = peerID
	log.Printf("Received offer from peer: %s", peerID)

	err := peerConnection.SetRemoteDescription(offer)
	if err != nil {
		log.Printf("Error setting remote description: %v", err)
		return
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Printf("Error creating answer: %v", err)
		return
	}

	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		log.Printf("Error setting local description: %v", err)
		return
	}

	answerJSON, err := json.Marshal(answer)
	if err != nil {
		log.Printf("Error marshaling answer: %v", err)
		return
	}

	signalingSrv.WriteJSON(Message{
		Type:    "answer",
		To:      peerID,
		Payload: answerJSON,
	})
}

func setupDataChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		log.Printf("Data channel '%s'-'%d' open. You can now send messages\n", dc.Label(), dc.ID())
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		fmt.Printf("Peer: %s\n", string(msg.Data))
	})

	dc.OnClose(func() {
		log.Println("Data channel closed")
		dataChannel = nil
	})
}
