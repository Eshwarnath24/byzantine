package main

// =============================================================================
// web.go — HTTP + SSE web dashboard for HotStuff BFT
//
// Serves a browser-based dashboard at http://localhost:7000+NodeID.
// Uses Server-Sent Events (SSE) for live push and POST for user input.
// Zero external dependencies — only Go standard library.
//
// Endpoints:
//   GET  /           — serves the HTML dashboard
//   GET  /events     — SSE stream of consensus events
//   POST /api/input  — accepts JSON {"value":"..."} and feeds it to inputLines
//   GET  /api/state  — returns a JSON snapshot of current consensus state
// =============================================================================

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

//go:embed dashboard.html
var dashboardHTML string

// ─── WEB EVENT SYSTEM ─────────────────────────────────────────────────────────

// WebEvent is a typed message pushed to all connected browser clients via SSE.
type WebEvent struct {
	Type string      `json:"type"`
	Data interface{} `json:"data,omitempty"`
}

// WebHub is a fan-out hub for Server-Sent Events.
type WebHub struct {
	mu   sync.Mutex
	subs map[chan WebEvent]bool
}

func newWebHub() *WebHub {
	return &WebHub{subs: make(map[chan WebEvent]bool)}
}

func (h *WebHub) subscribe() chan WebEvent {
	ch := make(chan WebEvent, 128)
	h.mu.Lock()
	h.subs[ch] = true
	h.mu.Unlock()
	return ch
}

func (h *WebHub) unsubscribe(ch chan WebEvent) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *WebHub) emit(evt WebEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- evt:
		default: // drop if client is slow
		}
	}
}

// ─── EMIT HELPERS ─────────────────────────────────────────────────────────────

// emitWeb pushes an event to all browser clients.
// Safe to call with or without cs.mu held (the hub has its own lock).
func (cs *ConsensusState) emitWeb(eventType string, data interface{}) {
	if cs.webHub == nil {
		return
	}
	cs.webHub.emit(WebEvent{Type: eventType, Data: data})
}

// emitState takes a state snapshot (acquires cs.mu) and pushes it via SSE.
// Do NOT call while cs.mu is already held — use emitStateLocked instead.
func (cs *ConsensusState) emitState() {
	if cs.webHub == nil {
		return
	}
	cs.mu.Lock()
	snap := cs.buildStateSnapshot()
	cs.mu.Unlock()
	cs.webHub.emit(WebEvent{Type: "state", Data: snap})
}

// emitStateLocked is like emitState but assumes cs.mu is already held.
func (cs *ConsensusState) emitStateLocked() {
	if cs.webHub == nil {
		return
	}
	snap := cs.buildStateSnapshot()
	cs.webHub.emit(WebEvent{Type: "state", Data: snap})
}

// buildStateSnapshot creates a JSON-friendly snapshot of consensus state.
// Must be called while cs.mu is held.
func (cs *ConsensusState) buildStateSnapshot() map[string]interface{} {
	leader := cs.leaderForView(cs.currentView)
	role := "replica"
	if leader == cs.NodeID {
		role = "leader"
	}

	peers := make([]map[string]interface{}, 0, len(cs.peerOrder))
	for _, id := range cs.peerOrder {
		peers = append(peers, map[string]interface{}{
			"id":     id,
			"port":   7000 + id,
			"leader": id == leader,
			"self":   id == cs.NodeID,
		})
	}

	committedSet := make(map[int]bool)
	for _, cid := range cs.committed {
		committedSet[cid] = true
	}

	qcSet := make(map[int]bool)
	for _, qc := range cs.qcs {
		qcSet[qc.BlockID] = true
	}

	blocks := make([]map[string]interface{}, 0, len(cs.qcs))
	for _, qc := range cs.qcs {
		blk := cs.blocks[qc.BlockID]
		parentID := 0
		data := qc.BlockData
		if blk != nil {
			parentID = blk.ParentID
			data = blk.Data
		}
		blocks = append(blocks, map[string]interface{}{
			"id":        qc.BlockID,
			"parentId":  parentID,
			"data":      data,
			"view":      qc.View,
			"committed": committedSet[qc.BlockID],
			"voteCount": qc.VoteCount,
			"leaderId":  qc.LeaderID,
			"voters":    qc.Voters,
		})
	}

	// Build full tree for visualization.
	treeNodes := cs.buildTreeJSON(cs.blockTree.root, committedSet, qcSet)

	// Reconstruct currently pending prompt so UI can recover after reconnects.
	var pendingPrompt map[string]interface{}
	if cs.isLeaderThisView() && cs.inRound && cs.pendingBlock == nil {
		pendingPrompt = map[string]interface{}{
			"type": "data",
			"data": map[string]interface{}{"view": cs.currentView},
		}
	} else if cs.proposalForView != nil && !cs.isLeaderThisView() {
		blk := cs.proposalForView
		expected := computeChecksum(blk.Data)
		pendingPrompt = map[string]interface{}{
			"type": "vote",
			"data": map[string]interface{}{
				"blockId":    blk.ID,
				"parentId":   blk.ParentID,
				"data":       blk.Data,
				"view":       blk.View,
				"leaderId":   blk.Proposer,
				"checksum":   blk.Checksum,
				"checksumOk": blk.Checksum == "" || blk.Checksum == expected,
			},
		}
	}

	return map[string]interface{}{
		"nodeId":        cs.NodeID,
		"port":          cs.Port,
		"view":          cs.currentView,
		"leader":        leader,
		"role":          role,
		"peers":         peers,
		"malicious":     cs.malicious,
		"committed":     len(cs.committed),
		"accepted":      len(cs.qcs),
		"blocks":        blocks,
		"tree":          treeNodes,
		"pendingPrompt": pendingPrompt,
		"lockedId":      cs.blockTree.LockedNodeID(),
		"highQCId":      cs.blockTree.HighQCNodeID(),
		"quorum":        cs.quorum(),
		"total":         cs.totalNodes(),
		"faultTol":      cs.faultTolerance(),
	}
}

// buildTreeJSON recursively serializes the block tree for the web dashboard.
func (cs *ConsensusState) buildTreeJSON(node *TreeNode, committedSet, qcSet map[int]bool) map[string]interface{} {
	if node == nil {
		return nil
	}
	blk := node.Block
	data := blk.Data
	if len(data) > 20 {
		data = data[:17] + "..."
	}
	children := make([]map[string]interface{}, 0, len(node.Children))
	for _, child := range node.Children {
		c := cs.buildTreeJSON(child, committedSet, qcSet)
		if c != nil {
			children = append(children, c)
		}
	}
	status := "pending"
	if committedSet[blk.ID] {
		status = "committed"
	} else if qcSet[blk.ID] {
		status = "qcd"
	}
	return map[string]interface{}{
		"id":       blk.ID,
		"parentId": blk.ParentID,
		"data":     data,
		"view":     blk.View,
		"proposer": blk.Proposer,
		"status":   status,
		"locked":   cs.blockTree.LockedNodeID() == blk.ID && blk.ID != 0,
		"highQC":   cs.blockTree.HighQCNodeID() == blk.ID && blk.ID != 0,
		"children": children,
	}
}

// ─── HTTP SERVER ──────────────────────────────────────────────────────────────

func (cs *ConsensusState) startWebServer() {
	webPort := 8000 + cs.NodeID

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, dashboardHTML)
	})
	mux.HandleFunc("/events", cs.handleSSE)
	mux.HandleFunc("/api/input", cs.handleInput)
	mux.HandleFunc("/api/state", cs.handleGetState)

	listenAddr := fmt.Sprintf("0.0.0.0:%d", webPort)
	logSys("Web dashboard → http://localhost:%d  (binding to %s)", webPort, listenAddr)
	go func() {
		if err := http.ListenAndServe(listenAddr, mux); err != nil {
			logErr("Web server failed on %s: %v", listenAddr, err)
		}
	}()
}

func (cs *ConsensusState) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher.Flush()

	ch := cs.webHub.subscribe()
	defer cs.webHub.unsubscribe(ch)

	// Immediately send current state.
	cs.mu.Lock()
	snap := cs.buildStateSnapshot()
	cs.mu.Unlock()
	initData, _ := json.Marshal(WebEvent{Type: "state", Data: snap})
	fmt.Fprintf(w, "data: %s\n\n", initData)
	flusher.Flush()

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (cs *ConsensusState) handleInput(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var cmd struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	select {
	case cs.inputLines <- cmd.Value:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "input channel full", http.StatusServiceUnavailable)
	}
}

func (cs *ConsensusState) handleGetState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	cs.mu.Lock()
	snap := cs.buildStateSnapshot()
	cs.mu.Unlock()
	json.NewEncoder(w).Encode(snap)
}
