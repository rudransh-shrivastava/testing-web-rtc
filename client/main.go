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
	isOfferer         bool
	pendingCandidates []webrtc.ICECandidateInit
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

	createNewPeerConnection := func() error {
		if peerConnection != nil {
			peerConnection.Close()
		}

		peerConnection, err = webrtc.NewPeerConnection(config)
		if err != nil {
			return err
		}

		peerConnection.OnICECandidate(func(ice *webrtc.ICECandidate) {
			if ice != nil && connectedPeer != "" {
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

		peerConnection.OnDataChannel(func(dc *webrtc.DataChannel) {
			log.Printf("Received data channel\n")
			dataChannel = dc
			setupDataChannel(dc)
		})

		peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			log.Printf("Connection state changed: %s\n", state.String())
		})

		return nil
	}

	err = createNewPeerConnection()
	if err != nil {
		log.Fatal(err)
	}
	defer peerConnection.Close()

	// Listen for messages from signaling server
	go handleSignalingMessages(createNewPeerConnection)

	// Handle user input
	fmt.Println("Type your messages (press Enter to send):")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()
		if dataChannel != nil && dataChannel.ReadyState() == webrtc.DataChannelStateOpen {
			dataChannel.SendText(text)
			fmt.Printf("You: %s\n", text)
		} else {
			fmt.Println("Not connected to peer yet")
		}
	}
}

func handleSignalingMessages(createNewPeerConnection func() error) {
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

			// Reset connection state if we see new peers
			if len(peers) > 0 && peers[0] != connectedPeer {
				connectedPeer = peers[0]
				isOfferer = myID > connectedPeer

				// Create new peer connection
				err := createNewPeerConnection()
				if err != nil {
					log.Printf("Error creating new peer connection: %v", err)
					continue
				}

				if isOfferer {
					// Wait a bit before creating offer to avoid race conditions
					time.Sleep(time.Second)
					log.Printf("Creating offer for peer: %s", connectedPeer)
					createOffer()
				} else {
					log.Printf("Waiting for offer from peer: %s", connectedPeer)
				}
			}

		case "offer":
			if msg.From == connectedPeer && !isOfferer {
				var offer webrtc.SessionDescription
				json.Unmarshal(msg.Payload, &offer)
				handleOffer(msg.From, offer)
			}

		case "answer":
			if msg.From == connectedPeer && isOfferer {
				var answer webrtc.SessionDescription
				json.Unmarshal(msg.Payload, &answer)
				err := peerConnection.SetRemoteDescription(answer)
				if err != nil {
					log.Printf("Error setting remote description: %v", err)
				}

				// Add any pending candidates
				for _, candidate := range pendingCandidates {
					err := peerConnection.AddICECandidate(candidate)
					if err != nil {
						log.Printf("Error adding pending ICE candidate: %v", err)
					}
				}
				pendingCandidates = nil
			}

		case "ice-candidate":
			if msg.From == connectedPeer {
				var candidate webrtc.ICECandidateInit
				json.Unmarshal(msg.Payload, &candidate)

				// If we haven't set the remote description yet, store the candidate
				if peerConnection.RemoteDescription() == nil {
					pendingCandidates = append(pendingCandidates, candidate)
				} else {
					err := peerConnection.AddICECandidate(candidate)
					if err != nil {
						log.Printf("Error adding ICE candidate: %v", err)
					}
				}
			}
		}
	}
}

func createOffer() {
	// Create data channel
	var err error
	dataChannel, err = peerConnection.CreateDataChannel("chat", nil)
	if err != nil {
		log.Printf("Error creating data channel: %v", err)
		return
	}
	setupDataChannel(dataChannel)

	// Create offer
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		log.Printf("Error creating offer: %v", err)
		return
	}

	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		log.Printf("Error setting local description: %v", err)
		return
	}

	offerJSON, err := json.Marshal(offer)
	if err != nil {
		log.Printf("Error marshaling offer: %v", err)
		return
	}

	signalingSrv.WriteJSON(Message{
		Type:    "offer",
		To:      connectedPeer,
		Payload: offerJSON,
	})
}

func handleOffer(peerID string, offer webrtc.SessionDescription) {
	log.Printf("Received offer from peer: %s", peerID)

	err := peerConnection.SetRemoteDescription(offer)
	if err != nil {
		log.Printf("Error setting remote description: %v", err)
		return
	}

	// Add any pending candidates
	for _, candidate := range pendingCandidates {
		err := peerConnection.AddICECandidate(candidate)
		if err != nil {
			log.Printf("Error adding pending ICE candidate: %v", err)
		}
	}
	pendingCandidates = nil

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
