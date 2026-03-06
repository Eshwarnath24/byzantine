package main

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// ═════════════════════════════════════════════════════════════════════════════
//  NETWORK LAYER — TCP Peer-to-Peer Implementation
//
//  Both nodes act as servers AND clients:
//    • Each node runs a TCP server listening on port 7000+nodeID
//    • Each node can connect as a client to other nodes' servers
//    • Connections are persistent and bidirectional
//    • Failed connections are retried automatically
//    • UDP beacon discovery helps nodes find each other on LAN
// ═════════════════════════════════════════════════════════════════════════════

const (
	tcpDialTimeout    = 5 * time.Second // Increased for cross-laptop latency
	httpClientTimeout = 5 * time.Second // Increased for cross-laptop latency
	udpBeaconPort     = 7999            // UDP port for peer discovery broadcasts
	udpBeaconInterval = 3 * time.Second // How often to broadcast presence
)

// connectionPool maintains persistent TCP connections to peers.
// Key = peerID, Value = net.Conn
var (
	connectionPool   = make(map[int]net.Conn)
	connectionPoolMu sync.RWMutex
)

// ─── TCP SERVER ───────────────────────────────────────────────────────────────

// startListener starts the TCP server on port 7000+nodeID.
// This allows the node to accept incoming connections from other nodes.
func (cs *ConsensusState) startListener() error {
	addr := fmt.Sprintf(":%d", cs.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	cs.listener = listener
	logNet("TCP server started on port %d", cs.Port)

	go cs.acceptLoop()
	return nil
}

// acceptLoop continuously accepts incoming TCP connections.
// Each connection is handled in a separate goroutine.
func (cs *ConsensusState) acceptLoop() {
	for {
		conn, err := cs.listener.Accept()
		if err != nil {
			select {
			case <-cs.stopCh:
				return
			default:
				logErr("Accept error: %v", err)
				continue
			}
		}

		// Handle each connection in a separate goroutine
		go cs.handleConnection(conn)
	}
}

// handleConnection reads messages from an incoming TCP connection.
// It expects JSON-encoded messages separated by newlines.
func (cs *ConsensusState) handleConnection(conn net.Conn) {
	defer conn.Close()

	decoder := json.NewDecoder(conn)
	var firstMessage bool = true
	var senderID int

	for {
		select {
		case <-cs.stopCh:
			return
		default:
		}

		// Set read deadline to detect dead connections
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))

		var msg Message
		if err := decoder.Decode(&msg); err != nil {
			if err, ok := err.(net.Error); ok && err.Timeout() {
				// Connection idle timeout - continue waiting
				continue
			}
			// Connection closed or decode error
			if senderID > 0 {
				logNet("Connection from Node %d closed", senderID)
			}
			return
		}

		// On first message, store the sender ID and register the connection
		if firstMessage && msg.SenderID > 0 {
			senderID = msg.SenderID
			firstMessage = false

			// Store this connection for sending messages back
			connectionPoolMu.Lock()
			if existing, exists := connectionPool[senderID]; exists {
				existing.Close() // Close old connection
			}
			connectionPool[senderID] = conn
			connectionPoolMu.Unlock()

			logNet("Registered incoming connection from Node %d (%s)", senderID, conn.RemoteAddr())
		}

		// Reset send failures on successful message receipt
		cs.mu.Lock()
		cs.sendFailures[msg.SenderID] = 0
		cs.mu.Unlock()

		// Process the message
		cs.handleMessage(msg)
	}
}

// ─── TCP CLIENT ───────────────────────────────────────────────────────────────

// peerAddr returns the network address for a given peer ID.
// If nodeAddrs contains an explicit IP, use it; otherwise use localhost.
func (cs *ConsensusState) peerAddr(peerID int) string {
	if peerID == cs.NodeID {
		return ""
	}

	cs.mu.Lock()
	host, hasExplicitIP := cs.nodeAddrs[peerID]
	cs.mu.Unlock()

	if !hasExplicitIP || host == "" {
		host = "localhost"
	}

	port := 7000 + peerID
	return fmt.Sprintf("%s:%d", host, port)
}

// getOrCreateConnection gets an existing connection or creates a new one to the peer.
// Returns the connection and a boolean indicating if it's a new connection.
func (cs *ConsensusState) getOrCreateConnection(peerID int) (net.Conn, error) {
	// Check if connection already exists
	connectionPoolMu.RLock()
	conn, exists := connectionPool[peerID]
	connectionPoolMu.RUnlock()

	if exists {
		return conn, nil // Assume alive; errors are caught during Encode
	}

	// Create new connection
	addr := cs.peerAddr(peerID)
	if addr == "" {
		return nil, fmt.Errorf("invalid peer address")
	}

	dialer := net.Dialer{Timeout: tcpDialTimeout}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial failed: %w", err)
	}

	// Store the new connection
	connectionPoolMu.Lock()
	connectionPool[peerID] = conn
	connectionPoolMu.Unlock()

	return conn, nil
}

// sendTo sends a message to a specific peer using TCP.
// Tracks consecutive failures and removes dead peers.
func (cs *ConsensusState) sendTo(peerID int, msg Message) {
	if peerID == cs.NodeID {
		return // Don't send to self
	}

	conn, err := cs.getOrCreateConnection(peerID)
	if err != nil {
		cs.incrementFailureCount(peerID)
		return
	}

	// Encode and send the message
	encoder := json.NewEncoder(conn)
	conn.SetWriteDeadline(time.Now().Add(tcpDialTimeout))
	err = encoder.Encode(msg)
	conn.SetWriteDeadline(time.Time{}) // Clear deadline

	if err != nil {
		// Send failed, close connection and increment failure count
		connectionPoolMu.Lock()
		delete(connectionPool, peerID)
		connectionPoolMu.Unlock()
		conn.Close()

		cs.incrementFailureCount(peerID)
		return
	}

	// Success - reset failure count
	cs.mu.Lock()
	cs.sendFailures[peerID] = 0
	cs.mu.Unlock()
}

// broadcast sends a message to all known peers.
func (cs *ConsensusState) broadcast(msg Message) {
	cs.mu.Lock()
	peerIDs := make([]int, 0, len(cs.peers))
	for id := range cs.peers {
		peerIDs = append(peerIDs, id)
	}

	// Also broadcast to configured but not-yet-connected peers in multi-laptop mode
	if len(cs.nodeAddrs) > 0 {
		for id := range cs.nodeAddrs {
			if id != cs.NodeID {
				found := false
				for _, existing := range peerIDs {
					if existing == id {
						found = true
						break
					}
				}
				if !found {
					peerIDs = append(peerIDs, id)
				}
			}
		}
	}
	cs.mu.Unlock()

	for _, peerID := range peerIDs {
		go cs.sendTo(peerID, msg)
	}
}

// incrementFailureCount increments the failure counter for a peer and removes it if threshold exceeded.
func (cs *ConsensusState) incrementFailureCount(peerID int) {
	cs.mu.Lock()
	cs.sendFailures[peerID]++
	failures := cs.sendFailures[peerID]
	cs.mu.Unlock()

	if failures >= maxSendFailures {
		logWarn("Node %d unreachable after %d attempts — removing from cluster", peerID, maxSendFailures)
		cs.removePeer(peerID, false)
	}
}

// removePeer removes a peer from the cluster and triggers a membership change.
func (cs *ConsensusState) removePeer(peerID int, graceful bool) {
	cs.mu.Lock()
	_, existed := cs.peers[peerID]
	if !existed {
		cs.mu.Unlock()
		return
	}

	delete(cs.peers, peerID)
	delete(cs.sendFailures, peerID)

	// Remove from join order
	newJoinOrder := []int{}
	for _, id := range cs.joinOrder {
		if id != peerID {
			newJoinOrder = append(newJoinOrder, id)
		}
	}
	cs.joinOrder = newJoinOrder

	oldCount := cs.totalNodes()
	cs.rebuildOrder()
	newCount := cs.totalNodes()

	wasInRound := cs.inRound
	wasLeader := cs.isLeaderThisView()
	cs.inRound = false
	cs.pendingBlock = nil
	cs.proposalForView = nil
	cs.votesForView = make(map[int]bool)
	cs.noVotesForView = make(map[int]bool)
	cs.stopViewTimer()
	cs.stopVotePhaseTimer()
	cs.mu.Unlock()

	// Close connection
	connectionPoolMu.Lock()
	if conn, exists := connectionPool[peerID]; exists {
		conn.Close()
		delete(connectionPool, peerID)
	}
	connectionPoolMu.Unlock()

	if graceful {
		logNet("Node %d left gracefully", peerID)
	} else {
		logErr("Node %d removed due to connectivity failure", peerID)
	}

	if oldCount != newCount && newCount > 0 {
		cs.mu.Lock()
		logSys("Cluster membership changed: %v  |  total=%d  quorum=%d  f=%d",
			cs.peerOrder, cs.totalNodes(), cs.quorum(), cs.faultTolerance())
		view := cs.currentView
		leader := cs.leaderForView(view)
		cs.mu.Unlock()

		if wasInRound || wasLeader {
			logWarn("Restarting round due to membership change")
			cs.broadcast(Message{Type: "NEW_VIEW", SenderID: cs.NodeID, View: view})
			if leader == cs.NodeID {
				go cs.runLeaderRound()
			} else {
				cs.mu.Lock()
				cs.inRound = true
				cs.startViewTimer()
				cs.mu.Unlock()
			}
		}
	}
}

// ─── PEER DISCOVERY ───────────────────────────────────────────────────────────

// discoverPeers attempts to connect to all configured peers.
// Called at startup and periodically to establish connections.
func (cs *ConsensusState) discoverPeers() {
	cs.mu.Lock()
	// In multi-laptop mode, try to connect to all configured peers
	peerIDs := make([]int, 0, len(cs.nodeAddrs))
	for id := range cs.nodeAddrs {
		if id != cs.NodeID {
			peerIDs = append(peerIDs, id)
		}
	}
	cs.mu.Unlock()

	if len(peerIDs) == 0 {
		// Same-machine mode: try localhost ports
		for id := 1; id <= 9; id++ {
			if id != cs.NodeID {
				peerIDs = append(peerIDs, id)
			}
		}
	}

	for _, peerID := range peerIDs {
		go cs.tryConnectToPeer(peerID)
	}
}

// tryConnectToPeer attempts to establish a connection to a peer and send HELLO.
func (cs *ConsensusState) tryConnectToPeer(peerID int) {
	addr := cs.peerAddr(peerID)
	if addr == "" {
		return
	}

	dialer := net.Dialer{Timeout: tcpDialTimeout}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		// Connection failed - this is normal if peer isn't running yet
		return
	}

	// Send HELLO message to establish identity
	msg := Message{
		Type:       "HELLO",
		SenderID:   cs.NodeID,
		SenderPort: cs.Port,
	}

	encoder := json.NewEncoder(conn)
	conn.SetWriteDeadline(time.Now().Add(tcpDialTimeout))
	err = encoder.Encode(msg)
	conn.SetWriteDeadline(time.Time{})

	if err != nil {
		connectionPoolMu.Lock()
		delete(connectionPool, peerID)
		connectionPoolMu.Unlock()
		conn.Close()
		return
	}

	// Start reading from this connection
	go cs.handleConnection(conn)
}

// discoveryLoop periodically attempts to discover and connect to new peers.
// Runs every 5 seconds to maintain connectivity.
func (cs *ConsensusState) discoveryLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-cs.stopCh:
			return
		case <-ticker.C:
			cs.discoverPeers()
		}
	}
}

// ─── UDP BEACON FOR LAN DISCOVERY ─────────────────────────────────────────────

// BeaconMessage is broadcast over UDP to help nodes find each other on the LAN.
type BeaconMessage struct {
	NodeID int    `json:"node_id"`
	IP     string `json:"ip"`
	Port   int    `json:"port"`
}

// startUDPBeacon broadcasts this node's presence on the LAN via UDP.
// Helps nodes discover each other without manual IP configuration.
func (cs *ConsensusState) startUDPBeacon() {
	// Get local IP address
	localIP := getLocalIP()
	if localIP == "" {
		return // Can't get local IP, skip beacon
	}

	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("255.255.255.255:%d", udpBeaconPort))
	if err != nil {
		return
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()

	beacon := BeaconMessage{
		NodeID: cs.NodeID,
		IP:     localIP,
		Port:   cs.Port,
	}

	ticker := time.NewTicker(udpBeaconInterval)
	defer ticker.Stop()

	for {
		select {
		case <-cs.stopCh:
			return
		case <-ticker.C:
			data, _ := json.Marshal(beacon)
			conn.Write(data)
		}
	}
}

// listenUDPBeacon listens for UDP beacons from other nodes on the LAN.
// When a beacon is received, attempts to connect to that node.
func (cs *ConsensusState) listenUDPBeacon() {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", udpBeaconPort))
	if err != nil {
		return
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return
	}
	defer conn.Close()

	buffer := make([]byte, 1024)

	for {
		select {
		case <-cs.stopCh:
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, _, err := conn.ReadFromUDP(buffer)
		if err != nil {
			continue
		}

		var beacon BeaconMessage
		if err := json.Unmarshal(buffer[:n], &beacon); err != nil {
			continue
		}

		// Ignore our own beacons
		if beacon.NodeID == cs.NodeID {
			continue
		}

		// Check if we already know about this node
		cs.mu.Lock()
		_, alreadyKnown := cs.nodeAddrs[beacon.NodeID]
		cs.mu.Unlock()

		if !alreadyKnown && beacon.IP != "" {
			logNet("Discovered Node %d on LAN at %s:%d via UDP beacon", beacon.NodeID, beacon.IP, beacon.Port)

			cs.mu.Lock()
			cs.nodeAddrs[beacon.NodeID] = beacon.IP
			cs.mu.Unlock()

			// Try to connect to this newly discovered peer
			go cs.tryConnectToPeer(beacon.NodeID)
		}
	}
}

// getLocalIP returns the local non-loopback IP address.
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}

	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return ""
}
