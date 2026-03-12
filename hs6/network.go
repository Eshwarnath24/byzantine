package main

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

const (
	tcpDialTimeout    = 5 * time.Second
	tcpReadTimeout    = 12 * time.Second
	httpClientTimeout = 5 * time.Second
	udpBeaconPort     = 7999
	udpBeaconInterval = 3 * time.Second
	failureResetAfter = 20 * time.Second
)

func (cs *ConsensusState) startListener() error {
	listener, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", cs.Port))
	if err != nil {
		return err
	}
	cs.listener = listener
	logNet("TCP server started on port %d", cs.Port)
	go cs.acceptLoop()
	return nil
}

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
		go cs.handleConnection(conn)
	}
}

func (cs *ConsensusState) handleConnection(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(tcpReadTimeout))
	dec := json.NewDecoder(conn)
	var msg Message
	if err := dec.Decode(&msg); err != nil {
		return
	}
	if msg.SenderID > 0 {
		cs.mu.Lock()
		cs.sendFailures[msg.SenderID] = 0
		// Auto-learn the sender's IP from the TCP connection so we can reply
		// even when the address was not pre-configured on the command line.
		if _, known := cs.nodeAddrs[msg.SenderID]; !known {
			if remoteAddr, _, err := net.SplitHostPort(conn.RemoteAddr().String()); err == nil && remoteAddr != "" {
				cs.nodeAddrs[msg.SenderID] = remoteAddr
			}
		}
		_, alreadyPeer := cs.peers[msg.SenderID]
		cs.mu.Unlock()
		// Only log the first connection from a peer; suppress repeat HELLO noise.
		if msg.Type != "PING" && !alreadyPeer {
			logNet("Registered incoming connection from Node %d (%s)", msg.SenderID, conn.RemoteAddr())
		}
	}
	cs.handleMessage(msg)
}

func (cs *ConsensusState) peerAddr(peerID int) string {
	if peerID == cs.NodeID {
		return ""
	}
	cs.mu.Lock()
	host := cs.nodeAddrs[peerID]
	cs.mu.Unlock()
	if host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("%s:%d", host, 7000+peerID)
}

func (cs *ConsensusState) sendTo(peerID int, msg Message) {
	if peerID == cs.NodeID {
		return
	}
	addr := cs.peerAddr(peerID)
	if addr == "" {
		return
	}
	if msg.StartOrderKey == 0 {
		cs.mu.Lock()
		msg.StartOrderKey = cs.myStartTime
		cs.mu.Unlock()
	}
	dialer := &net.Dialer{
		Timeout:   tcpDialTimeout,
		KeepAlive: 30 * time.Second,
	}
	conn, err := dialer.Dial("tcp4", addr)
	if err != nil {
		cs.incrementFailureCount(peerID, msg.Type)
		return
	}
	defer conn.Close()
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}
	enc := json.NewEncoder(conn)
	_ = conn.SetWriteDeadline(time.Now().Add(tcpDialTimeout))
	if err := enc.Encode(msg); err != nil {
		cs.incrementFailureCount(peerID, msg.Type)
		return
	}
	cs.mu.Lock()
	cs.sendFailures[peerID] = 0
	cs.mu.Unlock()
}

func (cs *ConsensusState) broadcast(msg Message) {
	cs.mu.Lock()
	peerIDs := make([]int, 0, len(cs.peers)+len(cs.nodeAddrs))
	seen := make(map[int]bool)
	for id := range cs.peers {
		if id != cs.NodeID {
			seen[id] = true
			peerIDs = append(peerIDs, id)
		}
	}
	for id := range cs.nodeAddrs {
		if id != cs.NodeID && !seen[id] {
			peerIDs = append(peerIDs, id)
		}
	}
	cs.mu.Unlock()
	for _, peerID := range peerIDs {
		go cs.sendTo(peerID, msg)
	}
}

func (cs *ConsensusState) shouldCountFailure(msgType string) bool {
	switch msgType {
	case "PING", "HELLO", "HELLO_ACK":
		return false
	default:
		return true
	}
}

func (cs *ConsensusState) incrementFailureCount(peerID int, msgType string) {
	if !cs.shouldCountFailure(msgType) {
		return
	}

	cs.mu.Lock()
	// Avoid carrying ancient failures forever; count only recent consecutive failures.
	if last, ok := cs.failureLastAt[peerID]; ok && time.Since(last) > failureResetAfter {
		cs.sendFailures[peerID] = 0
	}
	cs.sendFailures[peerID]++
	cs.failureLastAt[peerID] = time.Now()
	failures := cs.sendFailures[peerID]
	cs.mu.Unlock()
	if failures >= maxSendFailures {
		logWarn("Node %d unreachable after %d attempts - removing from cluster", peerID, maxSendFailures)
		cs.removePeer(peerID, false)
	}
}

func (cs *ConsensusState) removePeer(peerID int, graceful bool) {
	cs.mu.Lock()
	_, existed := cs.peers[peerID]
	if !existed {
		cs.mu.Unlock()
		return
	}
	delete(cs.peers, peerID)
	delete(cs.sendFailures, peerID)
	delete(cs.failureLastAt, peerID)
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
	view := cs.currentView
	leader := cs.leaderForView(view)
	cs.mu.Unlock()
	if graceful {
		logNet("Node %d left gracefully", peerID)
	} else {
		logErr("Node %d removed due to connectivity failure", peerID)
	}
	if oldCount != newCount && newCount > 0 {
		logSys("Cluster membership changed (first-seen): %v  |  total=%d  quorum=%d  f=%d", cs.peerOrder, cs.totalNodes(), cs.quorum(), cs.faultTolerance())
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

func (cs *ConsensusState) discoverPeers() {
	cs.mu.Lock()
	peerIDs := make([]int, 0, len(cs.nodeAddrs))
	hasConfiguredPeers := len(cs.nodeAddrs) > 0
	now := time.Now()
	for id := range cs.nodeAddrs {
		if id == cs.NodeID {
			continue
		}
		// Already a live peer — no need to re-send HELLO.
		if _, joined := cs.peers[id]; joined {
			continue
		}
		// Rate-limit: don't re-send HELLO within 15 s of the last attempt.
		if last, ok := cs.helloSentAt[id]; ok && now.Sub(last) < 15*time.Second {
			continue
		}
		peerIDs = append(peerIDs, id)
	}
	cs.mu.Unlock()

	if !hasConfiguredPeers {
		cs.mu.Lock()
		now2 := time.Now()
		for id := 1; id <= 9; id++ {
			if id == cs.NodeID {
				continue
			}
			if _, joined := cs.peers[id]; joined {
				continue
			}
			if last, ok := cs.helloSentAt[id]; ok && now2.Sub(last) < 15*time.Second {
				continue
			}
			peerIDs = append(peerIDs, id)
		}
		cs.mu.Unlock()
	}

	for _, peerID := range peerIDs {
		cs.mu.Lock()
		cs.helloSentAt[peerID] = time.Now()
		cs.mu.Unlock()
		go cs.tryConnectToPeer(peerID)
	}
}

func (cs *ConsensusState) tryConnectToPeer(peerID int) {
	cs.mu.Lock()
	timeMapCopy := make(map[int]int64, len(cs.startTime))
	for id, t := range cs.startTime {
		timeMapCopy[id] = t
	}
	myTime := cs.myStartTime
	cs.mu.Unlock()
	cs.sendTo(peerID, Message{
		Type:          "HELLO",
		SenderID:      cs.NodeID,
		SenderPort:    cs.Port,
		StartOrderKey: myTime,
		StartOrderMap: timeMapCopy,
	})
}

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

type BeaconMessage struct {
	NodeID int    `json:"node_id"`
	IP     string `json:"ip"`
	Port   int    `json:"port"`
}

func (cs *ConsensusState) startUDPBeacon() {
	localIP := getLocalIP()
	if localIP == "" {
		return
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
	beacon := BeaconMessage{NodeID: cs.NodeID, IP: localIP, Port: cs.Port}
	ticker := time.NewTicker(udpBeaconInterval)
	defer ticker.Stop()
	for {
		select {
		case <-cs.stopCh:
			return
		case <-ticker.C:
			data, _ := json.Marshal(beacon)
			_, _ = conn.Write(data)
		}
	}
}

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
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, _, err := conn.ReadFromUDP(buffer)
		if err != nil {
			continue
		}
		var beacon BeaconMessage
		if err := json.Unmarshal(buffer[:n], &beacon); err != nil {
			continue
		}
		if beacon.NodeID == cs.NodeID {
			continue
		}
		cs.mu.Lock()
		_, alreadyKnown := cs.nodeAddrs[beacon.NodeID]
		cs.mu.Unlock()
		if !alreadyKnown && beacon.IP != "" {
			logNet("Discovered Node %d on LAN at %s:%d via UDP beacon", beacon.NodeID, beacon.IP, beacon.Port)
			cs.mu.Lock()
			cs.nodeAddrs[beacon.NodeID] = beacon.IP
			cs.mu.Unlock()
			go cs.tryConnectToPeer(beacon.NodeID)
		}
	}
}

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
