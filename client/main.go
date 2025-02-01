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
)

func main() {
	// Connect to signaling server
	serverIP := os.Args[1]
	dialAddr := fmt.Sprintf("ws://%s:42069/ws", serverIP)
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
				To:      getPeerID(),
				Payload: candidateJSON,
			})
		}
	})

	// Listen for messages from signaling server
	go handleSignalingMessages()

	// Handle user input
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()
		if dataChannel != nil {
			dataChannel.SendText(text)
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
			log.Printf("Got ID: %s", myID)

		case "peers":
			var peers []string
			json.Unmarshal(msg.Payload, &peers)
			if len(peers) > 0 {
				// Create offer for the first available peer
				createOffer(peers[0])
			}

		case "offer":
			var offer webrtc.SessionDescription
			json.Unmarshal(msg.Payload, &offer)
			handleOffer(msg.From, offer)

		case "answer":
			var answer webrtc.SessionDescription
			json.Unmarshal(msg.Payload, &answer)
			peerConnection.SetRemoteDescription(answer)

		case "ice-candidate":
			var candidate webrtc.ICECandidateInit
			json.Unmarshal(msg.Payload, &candidate)
			peerConnection.AddICECandidate(candidate)
		}
	}
}

func createOffer(peerID string) {
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
	err := peerConnection.SetRemoteDescription(offer)
	if err != nil {
		log.Fatal(err)
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Fatal(err)
	}

	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		log.Fatal(err)
	}

	answerJSON, err := json.Marshal(answer)
	if err != nil {
		log.Fatal(err)
	}

	signalingSrv.WriteJSON(Message{
		Type:    "answer",
		To:      peerID,
		Payload: answerJSON,
	})
}

func setupDataChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		log.Printf("Data channel '%s'-'%d' open\n", dc.Label(), dc.ID())
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		fmt.Printf("Received: %s\n", string(msg.Data))
	})
}

func getPeerID() string {
	// In this simple example, we just return the first peer's ID
	var peers []string
	signalingSrv.WriteJSON(Message{Type: "get_peers"})
	var msg Message
	signalingSrv.ReadJSON(&msg)
	json.Unmarshal(msg.Payload, &peers)
	if len(peers) > 0 {
		return peers[0]
	}
	return ""
}
