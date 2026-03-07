package main

// ─────────────────────────────────────────────────────────────────────────────
//  dashboard.go  —  HTTP + WebSocket dashboard server for HotStuff BFT
//
//  Drop this file alongside hotstuff.go / network.go / tree.go, then rebuild:
//      go build -o hotstuff.exe .
//
//  Dashboard opens at:  http://localhost:<8000+nodeID>
//      Node 1  →  http://localhost:8001
//      Node 2  →  http://localhost:8002
// ─────────────────────────────────────────────────────────────────────────────

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	wsGUID        = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	wsFinalBit    = byte(0x80)
	wsOpText      = byte(0x01)
	wsOpClose     = byte(0x08)
	wsMaskBit     = byte(0x80)
	dashboardBase = 8000
)

// ── JSON payloads sent to the browser ────────────────────────────────────────

type DashNodeInfo struct {
	ID       int  `json:"id"`
	IsLeader bool `json:"is_leader"`
	IsYou    bool `json:"is_you"`
	Port     int  `json:"port"`
}

type DashBlock struct {
	ID        int    `json:"id"`
	ParentID  int    `json:"parent_id"`
	View      int    `json:"view"`
	Data      string `json:"data"`
	Checksum  string `json:"checksum"`
	Committed bool   `json:"committed"`
	QCd       bool   `json:"qcd"`
}

type DashState struct {
	Type           string         `json:"type"`
	NodeID         int            `json:"node_id"`
	View           int            `json:"view"`
	Leader         int            `json:"leader"`
	IsLeader       bool           `json:"is_leader"`
	Quorum         int            `json:"quorum"`
	TotalNodes     int            `json:"total_nodes"`
	CommittedCount int            `json:"committed_count"`
	AcceptedCount  int            `json:"accepted_count"`
	Peers          []DashNodeInfo `json:"peers"`
	Blocks         []DashBlock    `json:"blocks"`
	LockedBlock    int            `json:"locked_block"`
	HighQCBlock    int            `json:"high_qc_block"`
	PendingBlock   *DashBlock     `json:"pending_block,omitempty"`
	Proposal       *DashBlock     `json:"proposal,omitempty"`
	Malicious      bool           `json:"malicious"`
}

type DashEvent struct {
	Type    string `json:"type"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Time    string `json:"time"`
}

type DashVotePrompt struct {
	Type     string    `json:"type"`
	Block    DashBlock `json:"block"`
	LeaderID int       `json:"leader_id"`
	View     int       `json:"view"`
}

// ── WebSocket client ──────────────────────────────────────────────────────────

type wsClient struct {
	send chan []byte
	done chan struct{}
}

// ── Hub: fan-out to all connected browser tabs ───────────────────────────────

type DashHub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

func newDashHub() *DashHub { return &DashHub{clients: make(map[*wsClient]struct{})} }

func (h *DashHub) register(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *DashHub) unregister(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *DashHub) broadcastJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- b:
		default:
		}
	}
}

// ── Global hub registry (avoids modifying ConsensusState struct) ─────────────

var globalDashHubs sync.Map

func getDashHub(nodeID int) *DashHub {
	v, _ := globalDashHubs.Load(nodeID)
	if v == nil {
		return nil
	}
	return v.(*DashHub)
}

// ── Start the dashboard server ────────────────────────────────────────────────

func (cs *ConsensusState) startDashboard() {
	hub := newDashHub()
	globalDashHubs.Store(cs.NodeID, hub)

	port := dashboardBase + cs.NodeID
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cs.handleWS)
	mux.HandleFunc("/vote", cs.handleVoteHTTP)
	mux.HandleFunc("/propose", cs.handleProposeHTTP)
	mux.HandleFunc("/", serveDashboardHTML)

	logNet("Dashboard → http://localhost:%d", port)
	go func() {
		if err := http.ListenAndServe(fmt.Sprintf(":%d", port), mux); err != nil {
			logErr("Dashboard server: %v", err)
		}
	}()

	// 1-second state heartbeat
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-cs.stopCh:
				return
			case <-t.C:
				cs.dashPushState()
			}
		}
	}()
}

// ── Snapshot current state and push to all browsers ──────────────────────────

func (cs *ConsensusState) dashPushState() {
	hub := getDashHub(cs.NodeID)
	if hub == nil {
		return
	}
	cs.mu.Lock()
	s := DashState{
		Type:           "state",
		NodeID:         cs.NodeID,
		View:           cs.currentView,
		Leader:         cs.leaderForView(cs.currentView),
		IsLeader:       cs.isLeaderThisView(),
		Quorum:         cs.quorum(),
		TotalNodes:     cs.totalNodes(),
		CommittedCount: len(cs.committed),
		AcceptedCount:  len(cs.qcs),
		LockedBlock:    cs.blockTree.LockedNodeID(),
		HighQCBlock:    cs.blockTree.HighQCNodeID(),
		Malicious:      cs.malicious,
	}
	for _, id := range cs.peerOrder {
		s.Peers = append(s.Peers, DashNodeInfo{
			ID:       id,
			IsLeader: cs.leaderForView(cs.currentView) == id,
			IsYou:    id == cs.NodeID,
			Port:     7000 + id,
		})
	}
	committedSet := make(map[int]bool)
	for _, id := range cs.committed {
		committedSet[id] = true
	}
	for _, qc := range cs.qcs {
		if blk, ok := cs.blocks[qc.BlockID]; ok {
			node := cs.blockTree.GetNode(blk.ID)
			s.Blocks = append(s.Blocks, DashBlock{
				ID: blk.ID, ParentID: blk.ParentID,
				View: blk.View, Data: blk.Data, Checksum: blk.Checksum,
				Committed: committedSet[blk.ID],
				QCd:       node != nil && node.QCd,
			})
		}
	}
	if cs.pendingBlock != nil {
		b := cs.pendingBlock
		s.PendingBlock = &DashBlock{ID: b.ID, ParentID: b.ParentID, View: b.View, Data: b.Data, Checksum: b.Checksum}
	}
	if cs.proposalForView != nil {
		b := cs.proposalForView
		s.Proposal = &DashBlock{ID: b.ID, ParentID: b.ParentID, View: b.View, Data: b.Data, Checksum: b.Checksum}
	}
	cs.mu.Unlock()
	hub.broadcastJSON(s)
}

// ── Push event log line ───────────────────────────────────────────────────────

func (cs *ConsensusState) dashEvent(level, msg string) {
	hub := getDashHub(cs.NodeID)
	if hub == nil {
		return
	}
	hub.broadcastJSON(DashEvent{
		Type: "event", Level: level, Message: msg,
		Time: time.Now().Format("15:04:05"),
	})
}

// ── Push vote prompt (replica) ────────────────────────────────────────────────

func (cs *ConsensusState) dashVotePrompt(blk *Block, leaderID, view int) {
	hub := getDashHub(cs.NodeID)
	if hub == nil {
		return
	}
	hub.broadcastJSON(DashVotePrompt{
		Type: "vote_prompt", LeaderID: leaderID, View: view,
		Block: DashBlock{ID: blk.ID, ParentID: blk.ParentID, View: blk.View, Data: blk.Data, Checksum: blk.Checksum},
	})
}

// ── HTTP: replica submits vote from browser ───────────────────────────────────

func (cs *ConsensusState) handleVoteHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, 405)
		return
	}
	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"bad json"}`, 400)
		return
	}
	select {
	case cs.inputLines <- body.Answer:
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	default:
		http.Error(w, `{"error":"not waiting for vote right now"}`, 409)
	}
}

// ── HTTP: leader submits block data from browser ──────────────────────────────

func (cs *ConsensusState) handleProposeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, 405)
		return
	}
	var body struct {
		Data string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"bad json"}`, 400)
		return
	}
	select {
	case cs.inputLines <- body.Data:
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	default:
		http.Error(w, `{"error":"not waiting for propose right now"}`, 409)
	}
}

// ── Raw WebSocket implementation (RFC 6455, no external deps) ─────────────────

func wsHandshake(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, bool) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "not a websocket upgrade", 400)
		return nil, nil, false
	}
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", 500)
		return nil, nil, false
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, false
	}
	resp := strings.Join([]string{
		"HTTP/1.1 101 Switching Protocols",
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Accept: " + accept,
		"", "",
	}, "\r\n")
	_, _ = rw.WriteString(resp)
	_ = rw.Flush()
	return conn, rw, true
}

func wsSendText(rw *bufio.ReadWriter, data []byte) error {
	n := len(data)
	hdr := []byte{wsFinalBit | wsOpText}
	switch {
	case n <= 125:
		hdr = append(hdr, byte(n))
	case n <= 65535:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127, 0, 0, 0, 0,
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	if _, err := rw.Write(hdr); err != nil {
		return err
	}
	if _, err := rw.Write(data); err != nil {
		return err
	}
	return rw.Flush()
}

func wsReadFrame(rw *bufio.ReadWriter) (opcode byte, err error) {
	b0, e := rw.ReadByte()
	if e != nil {
		return 0, e
	}
	opcode = b0 & 0x0F
	b1, e := rw.ReadByte()
	if e != nil {
		return 0, e
	}
	masked := (b1 & wsMaskBit) != 0
	payLen := int(b1 & 0x7F)
	if payLen == 126 {
		buf := make([]byte, 2)
		if _, e = rw.Read(buf); e != nil {
			return 0, e
		}
		payLen = int(buf[0])<<8 | int(buf[1])
	} else if payLen == 127 {
		buf := make([]byte, 8)
		if _, e = rw.Read(buf); e != nil {
			return 0, e
		}
		payLen = int(buf[4])<<24 | int(buf[5])<<16 | int(buf[6])<<8 | int(buf[7])
	}
	var mask [4]byte
	if masked {
		if _, e = rw.Read(mask[:]); e != nil {
			return 0, e
		}
	}
	payload := make([]byte, payLen)
	if payLen > 0 {
		if _, e = rw.Read(payload); e != nil {
			return 0, e
		}
	}
	_ = payload
	return opcode, nil
}

func (cs *ConsensusState) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, rw, ok := wsHandshake(w, r)
	if !ok {
		return
	}
	defer conn.Close()

	hub := getDashHub(cs.NodeID)
	if hub == nil {
		return
	}
	client := &wsClient{send: make(chan []byte, 128), done: make(chan struct{})}
	hub.register(client)
	defer func() {
		hub.unregister(client)
		select {
		case <-client.done:
		default:
			close(client.done)
		}
	}()

	// Push current state immediately on first connect
	cs.dashPushState()

	// Write pump goroutine
	go func() {
		for {
			select {
			case data := <-client.send:
				if err := wsSendText(rw, data); err != nil {
					return
				}
			case <-client.done:
				return
			}
		}
	}()

	// Read pump: discard data, exit on close frame or error
	for {
		op, err := wsReadFrame(rw)
		if err != nil || op == wsOpClose {
			return
		}
	}
}

// ── Serve embedded HTML ───────────────────────────────────────────────────────

func serveDashboardHTML(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(dashboardHTML))
}
