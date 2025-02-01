package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

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
	myID              string
	signalingSrv      *websocket.Conn
	peerConnection    *webrtc.PeerConnection
	dataChannel       *webrtc.DataChannel
	mut               sync.Mutex
	connectedPeer     string
	pendingCandidates []webrtc.ICECandidateInit
)

func main() {
	serverIP := os.Args[1]
	dialAddr := fmt.Sprintf("ws://%s:42069/ws", serverIP)
	log.SetFlags(log.Ltime | log.Lshortfile)

	// Connect to signaling server
	var err error
	signalingSrv, _, err = websocket.DefaultDialer.Dial(dialAddr, nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer signalingSrv.Close()

	// Initialize WebRTC configuration
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	// Create initial peer connection
	peerConnection, err = webrtc.NewPeerConnection(config)
	if err != nil {
		log.Fatal(err)
	}

	// Set up peer connection handlers
	setupPeerConnection(peerConnection)

	// Listen for messages from signaling server
	go handleSignalingMessages()

	// Handle user input
	fmt.Println("Type your messages (press Enter to send):")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()
		if dataChannel != nil && dataChannel.ReadyState() == webrtc.DataChannelStateOpen {
			dataChannel.SendText(text)
			fmt.Printf("You: %s\n", text)
		} else {
			fmt.Println("Not connected to peer yet. Waiting for connection...")
		}
	}
}

func setupPeerConnection(pc *webrtc.PeerConnection) {
	// Handle ICE candidate generation
	pc.OnICECandidate(func(ice *webrtc.ICECandidate) {
		if ice != nil && connectedPeer != "" {
			candidateJSON, err := json.Marshal(ice.ToJSON())
			if err != nil {
				log.Printf("Failed to marshal ICE candidate: %v", err)
				return
			}
			signalingSrv.WriteJSON(Message{
				Type:    "ice-candidate",
				To:      connectedPeer,
				Payload: candidateJSON,
			})
		}
	})

	// Handle incoming data channels
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Received data channel: %s\n", dc.Label())
		dataChannel = dc
		setupDataChannel(dc)
	})

	// Monitor connection state changes
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Connection state changed to: %s\n", state.String())
		if state == webrtc.PeerConnectionStateFailed {
			// Connection failed, try to reconnect
			log.Println("Connection failed, requesting new peer list")
			signalingSrv.WriteJSON(Message{Type: "get_peers"})
		}
	})
}

func handleSignalingMessages() {
	for {
		var msg Message
		err := signalingSrv.ReadJSON(&msg)
		if err != nil {
			log.Printf("Signaling server read error: %v", err)
			return
		}

		switch msg.Type {
		case "id":
			var id string
			json.Unmarshal(msg.Payload, &id)
			myID = id
			log.Printf("Connected to server with ID: %s", myID)
			// Request peer list after getting ID
			signalingSrv.WriteJSON(Message{Type: "get_peers"})

		case "peers":
			var peers []string
			json.Unmarshal(msg.Payload, &peers)
			if len(peers) > 0 {
				peer := peers[0]
				if peer != connectedPeer {
					connectedPeer = peer
					if myID > peer {
						log.Printf("Initiating connection with peer: %s", peer)
						// Small delay to ensure both sides are ready
						time.Sleep(500 * time.Millisecond)
						initiateConnection()
					} else {
						log.Printf("Waiting for connection from peer: %s", peer)
					}
				}
			}

		case "offer":
			handleOffer(msg.From, msg.Payload)

		case "answer":
			handleAnswer(msg.From, msg.Payload)

		case "ice-candidate":
			handleICECandidate(msg.Payload)
		}
	}
}

func initiateConnection() {
	// Create data channel
	var err error
	dataChannel, err = peerConnection.CreateDataChannel("chat", nil)
	if err != nil {
		log.Printf("Failed to create data channel: %v", err)
		return
	}
	setupDataChannel(dataChannel)

	// Create offer
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		log.Printf("Failed to create offer: %v", err)
		return
	}

	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		log.Printf("Failed to set local description: %v", err)
		return
	}

	offerJSON, err := json.Marshal(offer)
	if err != nil {
		log.Printf("Failed to marshal offer: %v", err)
		return
	}

	signalingSrv.WriteJSON(Message{
		Type:    "offer",
		To:      connectedPeer,
		Payload: offerJSON,
	})
}

func handleOffer(from string, payload json.RawMessage) {
	var offer webrtc.SessionDescription
	err := json.Unmarshal(payload, &offer)
	if err != nil {
		log.Printf("Failed to unmarshal offer: %v", err)
		return
	}

	log.Printf("Received offer from: %s", from)
	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		log.Printf("Failed to set remote description: %v", err)
		return
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Printf("Failed to create answer: %v", err)
		return
	}

	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		log.Printf("Failed to set local description: %v", err)
		return
	}

	answerJSON, err := json.Marshal(answer)
	if err != nil {
		log.Printf("Failed to marshal answer: %v", err)
		return
	}

	signalingSrv.WriteJSON(Message{
		Type:    "answer",
		To:      from,
		Payload: answerJSON,
	})
}

func handleAnswer(from string, payload json.RawMessage) {
	var answer webrtc.SessionDescription
	err := json.Unmarshal(payload, &answer)
	if err != nil {
		log.Printf("Failed to unmarshal answer: %v", err)
		return
	}

	log.Printf("Received answer from: %s", from)
	err = peerConnection.SetRemoteDescription(answer)
	if err != nil {
		log.Printf("Failed to set remote description: %v", err)
		return
	}
}

func handleICECandidate(payload json.RawMessage) {
	var candidate webrtc.ICECandidateInit
	err := json.Unmarshal(payload, &candidate)
	if err != nil {
		log.Printf("Failed to unmarshal ICE candidate: %v", err)
		return
	}

	if peerConnection.RemoteDescription() == nil {
		pendingCandidates = append(pendingCandidates, candidate)
		return
	}

	err = peerConnection.AddICECandidate(candidate)
	if err != nil {
		log.Printf("Failed to add ICE candidate: %v", err)
	}
}

func setupDataChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		log.Printf("Data channel '%s' opened - you can now send messages\n", dc.Label())
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		fmt.Printf("Peer: %s\n", string(msg.Data))
	})

	dc.OnClose(func() {
		log.Printf("Data channel '%s' closed\n", dc.Label())
		dataChannel = nil
	})

	dc.OnError(func(err error) {
		log.Printf("Data channel error: %v", err)
	})
}
