package main

// =============================================================================
// network.go — TCP transport layer for HotStuff BFT
//
// Responsibilities:
//   • Peer address resolution (peerAddr)
//   • TCP listener (startListener / handleConn)
//   • Reliable unicast with failure tracking (sendTo / recordSendFailure)
//   • Dynamic peer removal on crash (removePeer)
//   • Broadcast to all live peers (broadcast)
//   • Startup peer discovery by port scan (discoverPeers)
//
// All methods are defined on *ConsensusState (declared in hotstuff.go) so
// they share the same mutex and protocol fields without any extra wiring.
// =============================================================================

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// peerAddr returns the TCP address for a given node ID.
// If a host was configured via the id=IP command-line argument it is used;
// otherwise "localhost" is assumed (same-machine / single-terminal mode).
func (cs *ConsensusState) peerAddr(id int) string {
	if host, ok := cs.nodeAddrs[id]; ok {
		return fmt.Sprintf("%s:%d", host, 7000+id)
	}
	return fmt.Sprintf("localhost:%d", 7000+id)
}

// ─── LISTENER ────────────────────────────────────────────────────────────────

// startListener opens a TCP server on this node's port and spawns a goroutine
// per incoming connection.
func (cs *ConsensusState) startListener() error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cs.Port))
	if err != nil {
		return err
	}
	cs.listener = ln
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-cs.stopCh:
					return
				default:
					continue
				}
			}
			go cs.handleConn(conn)
		}
	}()
	return nil
}

// handleConn decodes exactly one JSON message from the connection and
// dispatches it to the consensus message handler.
func (cs *ConsensusState) handleConn(conn net.Conn) {
	defer conn.Close()
	var msg Message
	if err := json.NewDecoder(conn).Decode(&msg); err != nil {
		return
	}
	cs.handleMessage(msg)
}

// ─── SEND / BROADCAST ────────────────────────────────────────────────────────

// sendTo (D) opens a fresh TCP connection, sends msg as JSON, then closes.
// Consecutive failures are tracked; the peer is removed after maxSendFailures.
func (cs *ConsensusState) sendTo(nodeID int, msg Message) {
	addr := cs.peerAddr(nodeID)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		cs.recordSendFailure(nodeID)
		return
	}
	defer conn.Close()
	if encErr := json.NewEncoder(conn).Encode(msg); encErr != nil {
		cs.recordSendFailure(nodeID)
		return
	}
	// Success: reset failure counter.
	cs.mu.Lock()
	cs.sendFailures[nodeID] = 0
	cs.mu.Unlock()
}

// recordSendFailure increments the per-peer failure counter and triggers
// peer removal after maxSendFailures consecutive failures.
func (cs *ConsensusState) recordSendFailure(nodeID int) {
	cs.mu.Lock()
	cs.sendFailures[nodeID]++
	fails := cs.sendFailures[nodeID]
	_, stillPeer := cs.peers[nodeID]
	cs.mu.Unlock()
	if fails >= maxSendFailures && stillPeer {
		logErr("Node %d unreachable (%dx) — removing from cluster", nodeID, fails)
		cs.removePeer(nodeID, true)
	}
}

// removePeer (D, E) — removes a crashed/departed node from the peer set and,
// when warranted, advances the view so the remaining nodes can make progress.
func (cs *ConsensusState) removePeer(nodeID int, restartRound bool) {
	cs.mu.Lock()
	if _, exists := cs.peers[nodeID]; !exists {
		cs.mu.Unlock()
		return
	}
	wasLeader := cs.leaderForView(cs.currentView) == nodeID
	delete(cs.peers, nodeID)
	delete(cs.sendFailures, nodeID)
	cs.rebuildOrder()
	fmt.Printf("\n%s%s%s\n", cYellow+cBold, bar(52), cReset)
	logWarn("Node %d removed from cluster", nodeID)
	logSys("Cluster: %v  total=%d  quorum=%d  f=%d",
		cs.peerOrder, cs.totalNodes(), cs.quorum(), cs.faultTolerance())
	fmt.Printf("%s%s%s\n", cYellow, bar(52), cReset)
	cs.emitStateLocked()
	cs.emitWeb("log", map[string]interface{}{"level": "warn", "message": fmt.Sprintf("Node %d removed from cluster", nodeID)})

	if !restartRound || (!wasLeader && !cs.inRound) {
		cs.mu.Unlock()
		return
	}

	// Advance view so the new membership is reflected immediately (E).
	newView := cs.currentView + 1
	cs.currentView = newView
	cs.inRound = false
	cs.pendingBlock = nil
	cs.proposalForView = nil
	cs.votesForView = make(map[int]bool)
	cs.noVotesForView = make(map[int]bool)
	cs.stopViewTimer()
	cs.stopVotePhaseTimer()
	newLeader := cs.leaderForView(newView)
	cs.mu.Unlock()

	logWarn("Membership change — restarting at view %d  |  leader: Node %d", newView, newLeader)
	cs.broadcast(Message{Type: "NEW_VIEW", SenderID: cs.NodeID, View: newView})
	if newLeader == cs.NodeID {
		go cs.runLeaderRound()
	} else {
		cs.mu.Lock()
		cs.inRound = true
		cs.startViewTimer()
		cs.mu.Unlock()
	}
}

// broadcast sends msg to every currently known peer (non-blocking, one
// goroutine per peer so a slow peer cannot block others).
func (cs *ConsensusState) broadcast(msg Message) {
	cs.mu.Lock()
	targets := make([]int, 0, len(cs.peers))
	for id := range cs.peers {
		targets = append(targets, id)
	}
	cs.mu.Unlock()
	for _, id := range targets {
		id := id
		go cs.sendTo(id, msg)
	}
}

// ─── PEER DISCOVERY ──────────────────────────────────────────────────────────

// discoverPeers attempts to connect to peer nodes at startup and sends HELLO
// to every node that is already listening.
//
// Multi-machine mode  (id=IP args were provided):  only the configured peer
// IDs are contacted, using their actual IP addresses.
//
// Same-machine mode   (no id=IP args):             scans localhost ports
// 8001–8009 as before, so existing single-machine usage is unchanged.
func (cs *ConsensusState) discoverPeers() {
	cs.mu.Lock()
	multiMachine := len(cs.nodeAddrs) > 0
	cs.mu.Unlock()

	if multiMachine {
		logSys("Multi-machine mode — scanning all ports (configured IPs + localhost co-nodes)...")
	} else {
		logSys("Scanning for peers on ports 8001–8009...")
	}

	// Always scan all IDs 1–9. peerAddr() uses the configured IP for
	// explicitly listed peers and falls back to localhost for the rest,
	// so co-located nodes on the same machine are always discovered.
	candidates := make([]int, 0, 9)
	for id := 1; id <= 9; id++ {
		if id != cs.NodeID {
			candidates = append(candidates, id)
		}
	}

	found := 0
	for _, id := range candidates {
		// Check if already known to avoid re-adding constantly
		cs.mu.Lock()
		_, alreadyKnown := cs.peers[id]
		cs.mu.Unlock()
		if alreadyKnown {
			continue // Skip peers we already have
		}

		addr := cs.peerAddr(id)
		conn, err := net.DialTimeout("tcp", addr, 1200*time.Millisecond)
		if err != nil {
			continue
		}
		conn.Close()
		logNet("Found Node %d at %s — sending HELLO", id, addr)
		cs.mu.Lock()
		cs.peers[id] = addr
		cs.rebuildOrder()
		cs.mu.Unlock()
		go cs.sendTo(id, Message{Type: "HELLO", SenderID: cs.NodeID})
		found++
	}
	if found == 0 {
		logSys("No new peers found.")
	} else {
		logSys("Discovered %d new peers.", found)
		time.Sleep(1000 * time.Millisecond)
	}
}

// discoveryLoop keeps trying peer discovery so nodes on different laptops can
// still connect even if they start at different times or have transient delays.
func (cs *ConsensusState) discoveryLoop() {
	ticker := time.NewTicker(3 * time.Second) // More aggressive: every 3 seconds
	defer ticker.Stop()
	for {
		select {
		case <-cs.stopCh:
			return
		case <-ticker.C:
			cs.discoverPeers()
			cs.tryConnectConfigPeers() // Also try to connect to nodeAddrs
		}
	}
}

// tryConnectConfigPeers actively tries to connect to nodes listed in nodeAddrs
// but not yet in peers. This ensures cross-laptop connections even if initial
// discovery fails.
func (cs *ConsensusState) tryConnectConfigPeers() {
	cs.mu.Lock()
	toTry := make([]int, 0, 9)
	for id := 1; id <= 9; id++ {
		if id != cs.NodeID {
			if _, knownAddr := cs.nodeAddrs[id]; knownAddr {
				if _, alreadyPeer := cs.peers[id]; !alreadyPeer {
					toTry = append(toTry, id)
				}
			}
		}
	}
	cs.mu.Unlock()

	if len(toTry) == 0 {
		return
	}

	for _, id := range toTry {
		addr := cs.peerAddr(id)
		conn, err := net.DialTimeout("tcp", addr, 800*time.Millisecond)
		if err != nil {
			continue // Will retry in 3 seconds
		}
		conn.Close()
		logNet("Reached from config: Node %d at %s — sending HELLO", id, addr)
		cs.mu.Lock()
		cs.peers[id] = addr
		cs.rebuildOrder()
		cs.mu.Unlock()
		go cs.sendTo(id, Message{Type: "HELLO", SenderID: cs.NodeID})
	}
}
