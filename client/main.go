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

	var err error
	signalingSrv, _, err = websocket.DefaultDialer.Dial(dialAddr, nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer signalingSrv.Close()

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{
					"stun:stun.l.google.com:19302",
					"stun:stun1.l.google.com:19302",
					"stun:stun2.l.google.com:19302",
					"stun:stun3.l.google.com:19302",
					"stun:stun4.l.google.com:19302",
				},
			},
		},
		ICETransportPolicy: webrtc.ICETransportPolicyAll,
	}

	peerConnection, err = webrtc.NewPeerConnection(config)
	if err != nil {
		log.Fatal(err)
	}
	defer peerConnection.Close()

	setupPeerConnection(peerConnection)

	go handleSignalingMessages()

	fmt.Println("Type your messages (press Enter to send):")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()
		if dataChannel != nil && dataChannel.ReadyState() == webrtc.DataChannelStateOpen {
			dataChannel.SendText(text)
			fmt.Printf("You: %s\n", text)
		} else {
			fmt.Println("Not connected yet. Waiting for connection...")
		}
	}
}

func setupPeerConnection(pc *webrtc.PeerConnection) {
	pc.OnICECandidate(func(ice *webrtc.ICECandidate) {
		if ice != nil && connectedPeer != "" {
			log.Printf("New ICE candidate: %s", ice.String())
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

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("ICE Connection State changed: %s", state.String())
	})

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Received data channel: %s", dc.Label())
		dataChannel = dc
		setupDataChannel(dc)
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Connection state changed to: %s", state.String())
		if state == webrtc.PeerConnectionStateFailed {
			log.Println("Connection failed, creating new peer connection")
			pc.Close()
			time.Sleep(time.Second)
			signalingSrv.WriteJSON(Message{Type: "get_peers"})
		}
	})

	pc.OnNegotiationNeeded(func() {
		log.Println("Negotiation needed")
		if myID > connectedPeer {
			initiateConnection()
		}
	})
}

func handleSignalingMessages() {
	for {
		var msg Message
		err := signalingSrv.ReadJSON(&msg)
		if err != nil {
			log.Printf("Signaling server error: %v", err)
			return
		}

		switch msg.Type {
		case "id":
			var id string
			json.Unmarshal(msg.Payload, &id)
			myID = id
			log.Printf("Connected with ID: %s", myID)
			signalingSrv.WriteJSON(Message{Type: "get_peers"})

		case "peers":
			var peers []string
			json.Unmarshal(msg.Payload, &peers)
			handlePeerList(peers)

		case "offer":
			if msg.From != connectedPeer {
				connectedPeer = msg.From
			}
			handleOffer(msg.From, msg.Payload)

		case "answer":
			handleAnswer(msg.From, msg.Payload)

		case "ice-candidate":
			if msg.From == connectedPeer {
				handleICECandidate(msg.Payload)
			}
		}
	}
}

func handlePeerList(peers []string) {
	if len(peers) == 0 {
		return
	}

	peer := peers[0]
	if peer == connectedPeer {
		return
	}

	connectedPeer = peer
	log.Printf("Potential peer found: %s", peer)

	if myID > peer {
		log.Printf("Initiating connection with: %s", peer)
		time.Sleep(500 * time.Millisecond)
		initiateConnection()
	} else {
		log.Printf("Waiting for connection from: %s", peer)
	}
}

func initiateConnection() {
	var err error
	dataChannel, err = peerConnection.CreateDataChannel("chat", nil)
	if err != nil {
		log.Printf("Data channel creation failed: %v", err)
		return
	}
	setupDataChannel(dataChannel)

	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		log.Printf("Offer creation failed: %v", err)
		return
	}

	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		log.Printf("Setting local description failed: %v", err)
		return
	}

	offerJSON, err := json.Marshal(offer)
	if err != nil {
		log.Printf("Offer marshaling failed: %v", err)
		return
	}

	log.Printf("Sending offer to: %s", connectedPeer)
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
		log.Printf("Offer unmarshal failed: %v", err)
		return
	}

	log.Printf("Got offer from: %s", from)
	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		log.Printf("Setting remote description failed: %v", err)
		return
	}

	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Printf("Answer creation failed: %v", err)
		return
	}

	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		log.Printf("Setting local description failed: %v", err)
		return
	}

	answerJSON, err := json.Marshal(answer)
	if err != nil {
		log.Printf("Answer marshaling failed: %v", err)
		return
	}

	log.Printf("Sending answer to: %s", from)
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
		log.Printf("Answer unmarshal failed: %v", err)
		return
	}

	log.Printf("Got answer from: %s", from)
	err = peerConnection.SetRemoteDescription(answer)
	if err != nil {
		log.Printf("Setting remote description failed: %v", err)
		return
	}
}

func handleICECandidate(payload json.RawMessage) {
	var candidate webrtc.ICECandidateInit
	err := json.Unmarshal(payload, &candidate)
	if err != nil {
		log.Printf("ICE candidate unmarshal failed: %v", err)
		return
	}

	if peerConnection.RemoteDescription() == nil {
		pendingCandidates = append(pendingCandidates, candidate)
		return
	}

	err = peerConnection.AddICECandidate(candidate)
	if err != nil {
		log.Printf("Adding ICE candidate failed: %v", err)
	}
}

func setupDataChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		log.Printf("Data channel '%s' open - you can now chat!", dc.Label())
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		fmt.Printf("Peer: %s\n", string(msg.Data))
	})

	dc.OnClose(func() {
		log.Printf("Data channel '%s' closed", dc.Label())
		dataChannel = nil
	})

	dc.OnError(func(err error) {
		log.Printf("Data channel error: %v", err)
	})
}
