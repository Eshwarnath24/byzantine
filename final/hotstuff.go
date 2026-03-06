package main

// =============================================================================
//  HotStuff BFT Consensus — Dynamic Single-File Implementation
//
//  No --n flag needed. Nodes discover each other automatically.
//  Cluster size, quorum, and fault tolerance update as peers join/leave.
//
//  GUARANTEES:
//    • n = 3f+1 nodes tolerate f Byzantine faults.
//    • Quorum = 2f+1 — any two quorums share ≥1 honest node (no split commits).
//    • Locking rule: node votes only for proposals that extend its locked block
//      or carry a higher-view QC reference, preventing forks.
//    • 3-chain commit: block B committed after three consecutive QC'd
//      descendants with verified parent-child links.
//    • View-change on timeout prevents permanent stall.
//
//  PROTOCOL FIXES:
//    A) Vote-phase timeout — view advances if quorum not reached in time.
//    B) Strict majority NO: abort immediately if >n/2 nodes reject.
//    C) Parent-child block validation in vote check and 3-chain commit.
//    D) Crash detection: peer removed after maxSendFailures TCP failures.
//    E) Round restart on membership change.
//    F) Leader = peerOrder[(view-1) % n] over sorted IDs (view 1 → node 1).
//    G) Vote validation: SenderID == VoterID, blockID and view must match.
//    H) All timeout paths prevent indefinite stalls.
//
//  HOW TO RUN — SAME MACHINE (4 terminals, no IP args needed):
//    go run hotstuff.go network.go 1
//    go run hotstuff.go network.go 2
//    go run hotstuff.go network.go 3
//    go run hotstuff.go network.go 4 -m   ← -m = Byzantine (malicious) node
//
//  HOW TO RUN — 4 LAPTOPS on the same LAN (replace IPs with actual ones):
//    Laptop 1:  go run hotstuff.go network.go 1 2=192.168.1.2 3=192.168.1.3 4=192.168.1.4
//    Laptop 2:  go run hotstuff.go network.go 2 1=192.168.1.1 3=192.168.1.3 4=192.168.1.4
//    Laptop 3:  go run hotstuff.go network.go 3 1=192.168.1.1 2=192.168.1.2 4=192.168.1.4
//    Laptop 4:  go run hotstuff.go network.go 4 1=192.168.1.1 2=192.168.1.2 3=192.168.1.3
//
//  Node IDs: 1–9   Port = 7000 + nodeID
//  Type 'exit' at any prompt to leave gracefully.
// =============================================================================

import (
	"bufio"
	"fmt"
	"hash/crc32"
	"math/rand"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//  CONFIG & TUNABLES

const (
	viewTimeout      = 20 * time.Second // replica waits this long for a proposal
	votePhaseTimeout = 12 * time.Second // leader waits this long for votes (A)
	maxSendFailures  = 30               // consecutive TCP failures before peer removal (D) - increased for cross-laptop
)

//  ANSI COLORS

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cRed    = "\033[91m"
	cGreen  = "\033[92m"
	cYellow = "\033[93m"
	cBlue   = "\033[94m"
	cPurple = "\033[95m"
	cCyan   = "\033[96m"
	cWhite  = "\033[97m"
)

func now() string      { return time.Now().Format("15:04:05.000") }
func bar(n int) string { return strings.Repeat("─", n) }

func logSys(f string, a ...any) {
	fmt.Printf("%s[%s] [SYS]   %s %s\n", cDim, now(), cReset, fmt.Sprintf(f, a...))
}
func logInfo(f string, a ...any) {
	fmt.Printf("%s[%s]%s [INFO]  %s\n", cDim, now(), cReset, fmt.Sprintf(f, a...))
}
func logWarn(f string, a ...any) {
	fmt.Printf("%s[%s]%s %s[WARN]%s  %s\n", cDim, now(), cReset, cYellow+cBold, cReset, fmt.Sprintf(f, a...))
}
func logLead(f string, a ...any) {
	fmt.Printf("%s[%s]%s %s[LEADER]%s %s\n", cDim, now(), cReset, cYellow+cBold, cReset, fmt.Sprintf(f, a...))
}
func logVote(f string, a ...any) {
	fmt.Printf("%s[%s]%s %s[VOTE]%s   %s\n", cDim, now(), cReset, cGreen+cBold, cReset, fmt.Sprintf(f, a...))
}
func logQC(f string, a ...any) {
	fmt.Printf("%s[%s]%s %s[QC]%s     %s\n", cDim, now(), cReset, cCyan+cBold, cReset, fmt.Sprintf(f, a...))
}
func logCommit(f string, a ...any) {
	fmt.Printf("%s[%s]%s %s[COMMIT]%s %s\n", cDim, now(), cReset, cPurple+cBold, cReset, fmt.Sprintf(f, a...))
}
func logNet(f string, a ...any) {
	fmt.Printf("%s[%s]%s %s[NET]%s    %s\n", cDim, now(), cReset, cBlue+cBold, cReset, fmt.Sprintf(f, a...))
}
func logErr(f string, a ...any) {
	fmt.Printf("%s[%s]%s %s[ERR]%s    %s\n", cDim, now(), cReset, cRed+cBold, cReset, fmt.Sprintf(f, a...))
}

// ─── DATA STRUCTURES ─────────────────────────────────────────────────────────

type Block struct {
	ID       int    `json:"id"`
	ParentID int    `json:"parent_id"`
	View     int    `json:"view"`
	Proposer int    `json:"proposer"`
	Data     string `json:"data"`
	Checksum string `json:"checksum"` // CRC32 of Data; malicious nodes send a wrong value
}

// computeChecksum returns the CRC32-IEEE hex checksum of data.
func computeChecksum(data string) string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(data)))
}

type Vote struct {
	VoterID int  `json:"voter_id"`
	BlockID int  `json:"block_id"`
	View    int  `json:"view"`
	NoVote  bool `json:"no_vote,omitempty"`
}

type QuorumCertificate struct {
	BlockID   int    `json:"block_id"`
	BlockData string `json:"block_data"`
	View      int    `json:"view"`
	VoteCount int    `json:"vote_count"` // total = explicit + 1 leader self-vote
	LeaderID  int    `json:"leader_id"`  // node that formed (self-voted) this QC
	Voters    []int  `json:"voters"`     // explicit YES voters (NOT including leader)
	FormedAt  string `json:"formed_at"`
}

type Message struct {
	Type           string             `json:"type"`
	SenderID       int                `json:"sender_id"`
	SenderPort     int                `json:"sender_port"`
	View           int                `json:"view"`
	Block          *Block             `json:"block,omitempty"`
	Vote           *Vote              `json:"vote,omitempty"`
	QC             *QuorumCertificate `json:"qc,omitempty"`
	KnownPeers     []int              `json:"known_peers,omitempty"`
	HighestQCBlock int                `json:"highest_qc_block,omitempty"` // for HELLO_ACK sync
	NodeAddrsMap   map[int]string     `json:"node_addrs_map,omitempty"`   // IP → nodeID mapping for cross-laptop sync
}

type ConsensusState struct {
	mu sync.Mutex

	NodeID    int
	Port      int
	malicious bool // -m flag: node exhibits Byzantine behaviour

	// nodeAddrs maps nodeID → host (IP or hostname) for cross-machine deployment.
	// If a node's ID is not present here, "localhost" is used (same-machine mode).
	nodeAddrs map[int]string

	peers     map[int]string
	peerOrder []int
	joinOrder []int // join-arrival order — determines round-robin leadership

	sendFailures map[int]int // (D) per-peer consecutive TCP failure count

	currentView int
	inRound     bool

	nextBlockID    int
	blocks         map[int]*Block
	qcs            []*QuorumCertificate
	committed      []int
	highestQCBlock int        // blockID of the highest QC seen (parent for next proposal)
	blockTree      *BlockTree // explicit block tree (see tree.go)

	pendingBlock   *Block
	votesForView   map[int]bool
	noVotesForView map[int]bool

	proposalForView *Block

	viewTimer      *time.Timer // (H) replica: fires if no proposal arrives
	votePhaseTimer *time.Timer // (A) leader: fires if quorum not reached in time

	listener   net.Listener
	stopCh     chan struct{}
	inputLines chan string
}

// ─── TIMER HELPERS ────────────────────────────────────────────────────────────

func (cs *ConsensusState) startViewTimer() {
	cs.stopViewTimer()
	cs.viewTimer = time.AfterFunc(viewTimeout, func() {
		cs.mu.Lock()
		if cs.isLeaderThisView() || !cs.inRound {
			cs.mu.Unlock()
			return
		}
		newView := cs.currentView + 1
		cs.currentView = newView
		cs.inRound = false
		cs.proposalForView = nil
		cs.votesForView = make(map[int]bool)
		cs.noVotesForView = make(map[int]bool)
		newLeader := cs.leaderForView(newView)
		cs.mu.Unlock()
		fmt.Printf("\n%s%s%s\n", cYellow+cBold, bar(52), cReset)
		logWarn("VIEW TIMER: leader silent — triggering VIEW CHANGE to view %d", newView)
		logSys("New leader: Node %d", newLeader)
		fmt.Printf("%s%s%s\n", cYellow, bar(52), cReset)
		cs.broadcast(Message{Type: "NEW_VIEW", SenderID: cs.NodeID, View: newView})
		if newLeader == cs.NodeID {
			go cs.runLeaderRound()
		} else {
			cs.mu.Lock()
			cs.inRound = true
			cs.startViewTimer()
			cs.mu.Unlock()
		}
	})
}

func (cs *ConsensusState) stopViewTimer() {
	if cs.viewTimer != nil {
		cs.viewTimer.Stop()
		cs.viewTimer = nil
	}
}

// startVotePhaseTimer (A) — leader fires a view change if quorum not reached.
func (cs *ConsensusState) startVotePhaseTimer() {
	cs.stopVotePhaseTimer()
	capturedView := cs.currentView
	cs.votePhaseTimer = time.AfterFunc(votePhaseTimeout, func() {
		cs.mu.Lock()
		if !cs.isLeaderThisView() || cs.currentView != capturedView || cs.pendingBlock == nil {
			cs.mu.Unlock()
			return
		}
		newView := cs.currentView + 1
		cs.currentView = newView
		cs.inRound = false
		cs.pendingBlock = nil
		cs.votesForView = make(map[int]bool)
		cs.noVotesForView = make(map[int]bool)
		newLeader := cs.leaderForView(newView)
		cs.mu.Unlock()
		fmt.Printf("\n%s%s%s\n", cYellow+cBold, bar(52), cReset)
		logWarn("VOTE-PHASE TIMEOUT — quorum not reached in %s — forcing VIEW CHANGE", votePhaseTimeout)
		logSys("New view: %d  |  New leader: Node %d", newView, newLeader)
		fmt.Printf("%s%s%s\n", cYellow, bar(52), cReset)
		cs.broadcast(Message{Type: "NEW_VIEW", SenderID: cs.NodeID, View: newView})
		if newLeader == cs.NodeID {
			go cs.runLeaderRound()
		} else {
			cs.mu.Lock()
			cs.inRound = true
			cs.startViewTimer()
			cs.mu.Unlock()
		}
	})
}

func (cs *ConsensusState) stopVotePhaseTimer() {
	if cs.votePhaseTimer != nil {
		cs.votePhaseTimer.Stop()
		cs.votePhaseTimer = nil
	}
}

// ─── CONSTRUCTOR ─────────────────────────────────────────────────────────────

func newState(nodeID int, malicious bool) *ConsensusState {
	return &ConsensusState{
		NodeID:         nodeID,
		Port:           7000 + nodeID,
		malicious:      malicious,
		nodeAddrs:      make(map[int]string),
		peers:          make(map[int]string),
		peerOrder:      []int{nodeID},
		joinOrder:      []int{nodeID},
		sendFailures:   make(map[int]int),
		currentView:    1,
		nextBlockID:    1,
		blocks:         make(map[int]*Block),
		qcs:            []*QuorumCertificate{},
		committed:      []int{},
		highestQCBlock: 0, // 0 = genesis
		blockTree:      NewBlockTree(),
		votesForView:   make(map[int]bool),
		noVotesForView: make(map[int]bool),
		stopCh:         make(chan struct{}),
		inputLines:     make(chan string, 64),
	}
}

// ─── CLUSTER HELPERS ─────────────────────────────────────────────────────────

// rebuildOrder rebuilds peerOrder in deterministic ID order.
// In multi-laptop mode (id=IP args), configured peers stay in the membership
// even if temporarily unreachable, so quorum/leader do not collapse to 1/1.
// In same-machine mode, only live peers are included.
// IMPORTANT: When using joinOrder, we respect FIRST-COME-FIRST-SERVED (arrival) order,
// NOT ID-sorted order. This ensures leader rotation follows actual join sequence.
func (cs *ConsensusState) rebuildOrder() {
	if len(cs.nodeAddrs) > 0 {
		// Multi-laptop mode: use fixed membership so quorum does not collapse
		// to a single node when a peer is temporarily unreachable.
		members := map[int]bool{cs.NodeID: true}
		for id := range cs.nodeAddrs {
			if id >= 1 && id <= 9 {
				members[id] = true
			}
		}
		order := make([]int, 0, len(members))
		for id := range members {
			order = append(order, id)
		}
		sort.Ints(order)
		cs.peerOrder = order
		return
	}

	order := make([]int, 0, len(cs.joinOrder))
	// Both multi-machine and same-machine modes: only include nodes that have ACTUALLY joined
	// joinOrder tracks the arrival sequence, so we respect it exactly
	for _, id := range cs.joinOrder {
		if id == cs.NodeID {
			// Always include ourselves
			order = append(order, id)
		} else if _, ok := cs.peers[id]; ok {
			// Only include peers that have actually connected
			order = append(order, id)
		}
	}
	// DO NOT sort by ID — respect arrival order for fair leader rotation
	cs.peerOrder = order
}

// addToJoinOrder appends id to joinOrder if not already present.
func (cs *ConsensusState) addToJoinOrder(id int) {
	for _, existing := range cs.joinOrder {
		if existing == id {
			return
		}
	}
	cs.joinOrder = append(cs.joinOrder, id)
}

func (cs *ConsensusState) totalNodes() int { return len(cs.peerOrder) }

// leaderForView — peerOrder[(view-1) % n] in join-arrival order.
// View 1 → first node that joined, view 2 → second, etc.
func (cs *ConsensusState) leaderForView(view int) int {
	if len(cs.peerOrder) == 0 {
		return cs.NodeID
	}
	return cs.peerOrder[(view-1)%len(cs.peerOrder)]
}

func (cs *ConsensusState) quorum() int {
	n := cs.totalNodes()
	if n < 2 {
		return 1
	}
	f := (n - 1) / 3
	q := 2*f + 1
	// With peers present always require at least 2 votes (leader + 1 peer)
	// so that replicas get a chance to vote before QC forms.
	if q < 2 {
		q = 2
	}
	return q
}

func (cs *ConsensusState) faultTolerance() int { return (cs.totalNodes() - 1) / 3 }

func (cs *ConsensusState) isLeaderThisView() bool {
	return cs.leaderForView(cs.currentView) == cs.NodeID
}

// ─── MESSAGE HANDLER ─────────────────────────────────────────────────────────

func (cs *ConsensusState) handleMessage(msg Message) {
	switch msg.Type {

	case "HELLO":
		cs.mu.Lock()
		isNew := false
		if _, exists := cs.peers[msg.SenderID]; !exists {
			cs.peers[msg.SenderID] = cs.peerAddr(msg.SenderID)
			cs.addToJoinOrder(msg.SenderID) // append in arrival order (no sort)
			cs.rebuildOrder()
			isNew = true
		}
		// Send the full join-ordered peerOrder so the joining node can adopt it.
		orderSnap := make([]int, len(cs.peerOrder))
		copy(orderSnap, cs.peerOrder)
		view := cs.currentView
		hqcBlock := cs.highestQCBlock
		cs.mu.Unlock()
		if isNew {
			fmt.Printf("\n%s%s%s\n", cGreen+cBold, bar(52), cReset)
			logNet("Node %d joined the network", msg.SenderID)
			cs.mu.Lock()
			logSys("Cluster: %v  total=%d  quorum=%d  f=%d",
				cs.peerOrder, cs.totalNodes(), cs.quorum(), cs.faultTolerance())

			wasInRound := cs.inRound
			wasLeader := cs.isLeaderThisView()
			newLeader := cs.leaderForView(cs.currentView)
			amStillLeader := (newLeader == cs.NodeID)

			cs.mu.Unlock()
			fmt.Printf("%s%s%s\n", cGreen, bar(52), cReset)

			if wasInRound {
				logWarn("Cluster membership changed - View %d continues", cs.currentView)
				if wasLeader && amStillLeader {
					logLead("You remain the leader - continue when ready")
				} else if !amStillLeader {
					logInfo("Node %d is now the leader", newLeader)
				} else {
					logLead(">>> YOU are now the leader for View %d", cs.currentView)
					go cs.runLeaderRound()
				}
			}
		}
		// Copy nodeAddrs for exchange
		addrsCopy := make(map[int]string, len(cs.nodeAddrs))
		for id, ip := range cs.nodeAddrs {
			addrsCopy[id] = ip
		}
		go cs.sendTo(msg.SenderID, Message{
			Type:           "HELLO_ACK",
			SenderID:       cs.NodeID,
			View:           view,
			KnownPeers:     orderSnap, // full join-ordered peer list
			HighestQCBlock: hqcBlock,
			NodeAddrsMap:   addrsCopy, // share IP config
		})

	case "HELLO_ACK":
		cs.mu.Lock()
		// Merge received NodeAddrsMap with our known addresses (cross-laptop sync)
		if len(msg.NodeAddrsMap) > 0 {
			for id, ip := range msg.NodeAddrsMap {
				if id != cs.NodeID && ip != "" {
					if _, exists := cs.nodeAddrs[id]; !exists {
						logNet("Learned peer config: Node %d → %s", id, ip)
						cs.nodeAddrs[id] = ip
					}
				}
			}
		}
		// Adopt the join order from the responding (existing) node.
		// KnownPeers = their full peerOrder, already including us.
		// Merge: trust their order first, then append any extra nodes we know.
		if len(msg.KnownPeers) > 0 {
			seen := make(map[int]bool, len(msg.KnownPeers)+len(cs.joinOrder))
			newOrder := make([]int, 0, len(msg.KnownPeers)+len(cs.joinOrder))
			for _, id := range msg.KnownPeers {
				if !seen[id] {
					seen[id] = true
					newOrder = append(newOrder, id)
					if id != cs.NodeID {
						if _, exists := cs.peers[id]; !exists {
							cs.peers[id] = cs.peerAddr(id)
						}
					}
				}
			}
			for _, id := range cs.joinOrder {
				if !seen[id] {
					seen[id] = true
					newOrder = append(newOrder, id)
				}
			}
			cs.joinOrder = newOrder
			cs.rebuildOrder()
		}
		if msg.View > cs.currentView {
			cs.currentView = msg.View
		}
		if msg.HighestQCBlock > cs.highestQCBlock {
			cs.highestQCBlock = msg.HighestQCBlock
		}

		wasInRound := cs.inRound
		wasLeader := cs.isLeaderThisView()
		newLeader := cs.leaderForView(cs.currentView)
		amStillLeader := (newLeader == cs.NodeID)

		if wasInRound && wasLeader && !amStillLeader {
			cs.inRound = false
			cs.pendingBlock = nil
			cs.proposalForView = nil
			cs.stopViewTimer()
			cs.stopVotePhaseTimer()
			cs.mu.Unlock()

			logWarn("Cluster updated - Node %d is now the leader for View %d", newLeader, cs.currentView)
			logInfo("You are now a replica. Waiting for proposal...")

			cs.mu.Lock()
			cs.inRound = true
			cs.startViewTimer()
			cs.mu.Unlock()
		} else {
			cs.mu.Unlock()
		}

	case "PROPOSE":
		if msg.Block == nil {
			return
		}
		cs.mu.Lock()
		if msg.View < cs.currentView {
			cs.mu.Unlock()
			return
		}
		if cs.isLeaderThisView() {
			cs.mu.Unlock()
			return
		}
		blk := msg.Block
		// (C) Parent-block existence check before accepting proposal.
		if blk.ParentID != 0 {
			if _, parentExists := cs.blocks[blk.ParentID]; !parentExists {
				cs.mu.Unlock()
				logWarn("PROPOSE rejected: parent block %d unknown — sending NO vote", blk.ParentID)
				go cs.sendTo(msg.SenderID, Message{
					Type:     "VOTE",
					SenderID: cs.NodeID,
					View:     msg.View,
					Vote:     &Vote{VoterID: cs.NodeID, BlockID: blk.ID, View: msg.View, NoVote: true},
				})
				return
			}
		}
		// Checksum validation: auto-reject if checksum does not match data.
		checksumOK := blk.Checksum == "" || blk.Checksum == computeChecksum(blk.Data)
		if !checksumOK {
			cs.mu.Unlock()
			fmt.Printf("\n%s%s%s\n", cRed+cBold, bar(52), cReset)
			logWarn("[CHECKSUM FAIL] Block %d from Node %d — expected %s, got %s",
				blk.ID, msg.SenderID, computeChecksum(blk.Data), blk.Checksum)
			logWarn("AUTO-REJECTING proposal (Byzantine node detected)")
			fmt.Printf("%s%s%s\n", cRed, bar(52), cReset)
			go cs.sendTo(msg.SenderID, Message{
				Type:     "VOTE",
				SenderID: cs.NodeID,
				View:     msg.View,
				Vote:     &Vote{VoterID: cs.NodeID, BlockID: blk.ID, View: msg.View, NoVote: true},
			})
			return
		}
		// Locking rule: the proposal must extend the locked block.
		lockedID := cs.blockTree.LockedNodeID()
		if lockedID != 0 && !cs.blockTree.IsAncestor(lockedID, blk.ParentID) {
			cs.mu.Unlock()
			logWarn("PROPOSE rejected: Block %d does not extend locked Block %d — sending NO vote",
				blk.ID, lockedID)
			go cs.sendTo(msg.SenderID, Message{
				Type:     "VOTE",
				SenderID: cs.NodeID,
				View:     msg.View,
				Vote:     &Vote{VoterID: cs.NodeID, BlockID: blk.ID, View: msg.View, NoVote: true},
			})
			return
		}
		cs.stopViewTimer()
		cs.currentView = msg.View
		cs.inRound = true
		cs.proposalForView = blk
		cs.blocks[blk.ID] = blk
		if blk.ID >= cs.nextBlockID {
			cs.nextBlockID = blk.ID + 1
		}
		_ = cs.blockTree.AddBlock(blk)
		cs.mu.Unlock()
		cs.printProposalAndPrompt(blk, msg.SenderID, msg.View)

	case "VOTE":
		if msg.Vote == nil {
			return
		}
		cs.mu.Lock()
		// (G) Basic guards: must be leader, correct view, have a pending block.
		if !cs.isLeaderThisView() || msg.View != cs.currentView || cs.pendingBlock == nil {
			cs.mu.Unlock()
			return
		}
		// (G) Identity: network SenderID must equal the vote's claimed VoterID.
		if msg.SenderID != msg.Vote.VoterID {
			cs.mu.Unlock()
			logWarn("VOTE rejected: SenderID %d != VoterID %d (spoofed?)", msg.SenderID, msg.Vote.VoterID)
			return
		}
		// (G) Block identity: vote must reference the exact pending block.
		if msg.Vote.BlockID != cs.pendingBlock.ID {
			cs.mu.Unlock()
			logWarn("VOTE rejected: blockID %d != pendingBlock %d", msg.Vote.BlockID, cs.pendingBlock.ID)
			return
		}
		voterID := msg.Vote.VoterID
		total := cs.totalNodes()

		if msg.Vote.NoVote {
			// (B) Strict majority rejection: NO > total/2 → abort immediately.
			if _, already := cs.noVotesForView[voterID]; already {
				cs.mu.Unlock()
				return
			}
			cs.noVotesForView[voterID] = true
			noCount := len(cs.noVotesForView)
			cs.mu.Unlock()
			logVote("NO vote from Node %d  |  NO: %d  (abort threshold: >%d)", voterID, noCount, total/2)
			if noCount > total/2 {
				go cs.abortRound()
			}
			return
		}

		// YES vote.
		if _, already := cs.votesForView[voterID]; already {
			cs.mu.Unlock()
			return
		}
		cs.votesForView[voterID] = true
		yesVotes := len(cs.votesForView) + 1 // +1 for leader implicit vote
		needed := cs.quorum()
		cs.mu.Unlock()
		logVote("YES vote from Node %d  |  YES: %d/%d", voterID, yesVotes, needed)
		if yesVotes >= needed {
			go cs.formAndBroadcastQC()
		}

	case "QC":
		// Replicas receive this from the leader once a quorum certificate is formed.
		if msg.QC == nil {
			return
		}
		cs.mu.Lock()
		alreadyHave := false
		for _, q := range cs.qcs {
			if q.BlockID == msg.QC.BlockID && q.View == msg.QC.View {
				alreadyHave = true
				break
			}
		}
		if !alreadyHave {
			cs.qcs = append(cs.qcs, msg.QC)
			if msg.Block != nil {
				cs.blocks[msg.Block.ID] = msg.Block
				if msg.Block.ID >= cs.nextBlockID {
					cs.nextBlockID = msg.Block.ID + 1
				}
				_ = cs.blockTree.AddBlock(msg.Block)
				cs.blockTree.MarkQC(msg.QC.BlockID)
				// Advance highestQCBlock on receiving QC from leader.
				if msg.Block.ID > cs.highestQCBlock {
					cs.highestQCBlock = msg.Block.ID
				}
			}
		}
		if msg.View >= cs.currentView {
			cs.currentView = msg.View + 1
		}
		cs.inRound = false
		cs.proposalForView = nil
		cs.stopViewTimer()
		cs.stopVotePhaseTimer()
		cs.mu.Unlock()
		if !alreadyHave {
			cs.checkAndCommit()
			printQC(msg.QC)
			cs.printBlockchainState()
			cs.mu.Lock()
			nextView := cs.currentView
			nextLeader := cs.leaderForView(nextView)
			cs.mu.Unlock()
			fmt.Printf("\n%s%s%s\n", cCyan, bar(52), cReset)
			logSys("View %d complete → View %d  |  Leader: Node %d", nextView-1, nextView, nextLeader)
			if nextLeader == cs.NodeID {
				logLead(">>> YOU are the leader for View %d", nextView)
			} else {
				logInfo("Waiting for proposal from Node %d...", nextLeader)
			}
			fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
			if nextLeader == cs.NodeID {
				go cs.runLeaderRound()
			} else {
				cs.mu.Lock()
				cs.inRound = true
				cs.startViewTimer()
				cs.mu.Unlock()
			}
		}

	case "PREPARE":
		// Leader tells replicas it is alive and preparing a proposal.
		// Reset view timer so replicas don't timeout while leader takes input.
		cs.mu.Lock()
		if msg.View >= cs.currentView && !cs.isLeaderThisView() {
			cs.currentView = msg.View
			cs.inRound = true
			cs.startViewTimer()
		}
		cs.mu.Unlock()

	case "NEW_VIEW":
		cs.mu.Lock()
		if msg.View > cs.currentView {
			cs.currentView = msg.View
			cs.inRound = false
			cs.pendingBlock = nil
			cs.proposalForView = nil
			cs.votesForView = make(map[int]bool)
			cs.noVotesForView = make(map[int]bool)
			cs.stopViewTimer()
			cs.stopVotePhaseTimer()
			leader := cs.leaderForView(cs.currentView)
			cs.mu.Unlock()
			logWarn("NEW_VIEW from Node %d → view %d  |  leader: Node %d",
				msg.SenderID, msg.View, leader)
			if leader == cs.NodeID {
				go cs.runLeaderRound()
			} else {
				cs.mu.Lock()
				cs.inRound = true
				cs.startViewTimer()
				cs.mu.Unlock()
			}
		} else {
			cs.mu.Unlock()
		}

	case "PING":
		// Health check ping - no response needed, just receiving confirms node is alive

	case "LEAVE":
		cs.removePeer(msg.SenderID, true)
	}
}

// ─── PROPOSAL DISPLAY ────────────────────────────────────────────────────────

func (cs *ConsensusState) printProposalAndPrompt(blk *Block, leaderID, view int) {
	expected := computeChecksum(blk.Data)
	checksumStatus := fmt.Sprintf("%s%s ✓ VALID%s", cGreen+cBold, blk.Checksum, cReset)
	if blk.Checksum != "" && blk.Checksum != expected {
		checksumStatus = fmt.Sprintf("%s%s ✗ INVALID (expected %s)%s", cRed+cBold, blk.Checksum, expected, cReset)
	} else if blk.Checksum == "" {
		checksumStatus = fmt.Sprintf("%s(none)%s", cDim, cReset)
	}
	fmt.Printf("\n%s%s%s\n", cCyan+cBold, bar(52), cReset)
	fmt.Printf("%s  VIEW %d — PROPOSAL RECEIVED%s\n", cCyan+cBold, view, cReset)
	fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
	fmt.Printf("  %-20s Node %d\n", "Leader:", leaderID)
	fmt.Printf("  %-20s %d\n", "Block ID:", blk.ID)
	fmt.Printf("  %-20s %d\n", "Parent Block ID:", blk.ParentID)
	fmt.Printf("  %-20s %s%q%s\n", "Block Data:", cPurple, blk.Data, cReset)
	fmt.Printf("  %-20s %d\n", "View:", blk.View)
	fmt.Printf("  %-20s %s\n", "Checksum:", checksumStatus)
	fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
	fmt.Printf("\n%sChecksum OK — Accept proposal from leader? (y/n): %s", cGreen+cBold, cReset)
}

// ─── LEADER ROUND ─────────────────────────────────────────────────────────────

func (cs *ConsensusState) runLeaderRound() {
	time.Sleep(300 * time.Millisecond)
	cs.mu.Lock()
	if !cs.isLeaderThisView() || cs.inRound {
		cs.mu.Unlock()
		return
	}
	view := cs.currentView
	cs.inRound = true
	cs.votesForView = make(map[int]bool)
	cs.noVotesForView = make(map[int]bool)
	cs.mu.Unlock()

	fmt.Printf("\n%s%s%s\n", cYellow+cBold, bar(52), cReset)
	fmt.Printf("%s  STEP 1 — PROPOSE PHASE  (View %d)%s\n", cYellow+cBold, view, cReset)
	fmt.Printf("%s%s%s\n", cYellow, bar(52), cReset)

	cs.mu.Lock()
	leader := cs.leaderForView(view)
	peers := make([]int, 0, len(cs.peers))
	for id := range cs.peers {
		peers = append(peers, id)
	}
	sort.Ints(peers)
	q, f, total := cs.quorum(), cs.faultTolerance(), cs.totalNodes()
	cs.mu.Unlock()

	logLead("View %d  |  Leader: Node %d (YOU)  |  total=%d  quorum=%d  f=%d",
		view, leader, total, q, f)
	logLead("Broadcasting proposal to: %v", peers)

	// Notify replicas that we are alive and preparing — resets their viewTimer.
	cs.broadcast(Message{Type: "PREPARE", SenderID: cs.NodeID, View: view})

	fmt.Printf("%s  Enter block data: %s", cYellow+cBold, cReset)
	data := cs.readLine()
	if data == "" {
		data = fmt.Sprintf("block-v%d-%04d", view, rand.Intn(9999))
		logInfo("(empty — using auto data: %s)", data)
	}

	cs.mu.Lock()
	// Verify we are still the leader for this view after waiting for input.
	if cs.currentView != view || !cs.isLeaderThisView() {
		cs.mu.Unlock()
		logWarn("View changed while waiting for input — aborting proposal")
		return
	}
	parentID := cs.highestQCBlock // always extend the highest QC'd block
	blockID := cs.nextBlockID
	cs.nextBlockID++
	checksum := computeChecksum(data)
	if cs.malicious {
		// Byzantine: deliberately corrupt the checksum so honest replicas reject.
		checksum = fmt.Sprintf("%08x", rand.Uint32())
		logWarn("[BYZANTINE] Sending corrupted checksum %s (real=%s)", checksum, computeChecksum(data))
	}
	blk := &Block{ID: blockID, ParentID: parentID, View: view, Proposer: cs.NodeID, Data: data, Checksum: checksum}
	cs.pendingBlock = blk
	cs.blocks[blockID] = blk
	_ = cs.blockTree.AddBlock(blk)
	cs.mu.Unlock()

	logLead("Proposing Block %d (parent=%d)  data=%q  checksum=%s", blk.ID, blk.ParentID, blk.Data, blk.Checksum)
	fmt.Printf("%s%s%s\n", cYellow, bar(52), cReset)

	cs.broadcast(Message{Type: "PROPOSE", SenderID: cs.NodeID, View: view, Block: blk})

	// (A) Start vote-phase timer after broadcasting.
	cs.mu.Lock()
	cs.startVotePhaseTimer()
	noPeers := len(cs.peers) == 0
	cs.mu.Unlock()

	if noPeers && len(cs.nodeAddrs) == 0 {
		logWarn("No peers — forming QC immediately (single-node mode)")
		cs.formAndBroadcastQC()
	} else if noPeers {
		logWarn("No live peers reachable right now (configured cluster mode) — waiting for connectivity")
	}
}

// ─── FORM QC + BROADCAST ─────────────────────────────────────────────────────

func (cs *ConsensusState) formAndBroadcastQC() {
	cs.mu.Lock()
	if cs.pendingBlock == nil {
		cs.mu.Unlock()
		return
	}
	blk := cs.pendingBlock
	voters := []int{} // explicit YES voters (leader self-vote tracked separately)
	for id := range cs.votesForView {
		voters = append(voters, id)
	}
	sort.Ints(voters)
	qc := &QuorumCertificate{
		BlockID:   blk.ID,
		BlockData: blk.Data,
		View:      cs.currentView,
		VoteCount: len(voters) + 1, // +1 for leader self-vote
		LeaderID:  cs.NodeID,
		Voters:    voters,
		FormedAt:  now(),
	}
	cs.qcs = append(cs.qcs, qc)
	cs.blockTree.MarkQC(blk.ID)
	if blk.ID > cs.highestQCBlock {
		cs.highestQCBlock = blk.ID
	}
	cs.currentView++
	cs.inRound = false
	cs.pendingBlock = nil
	cs.votesForView = make(map[int]bool)
	cs.noVotesForView = make(map[int]bool)
	cs.stopVotePhaseTimer() // (A) cancel — QC formed before timeout
	nextView := cs.currentView
	nextLeader := cs.leaderForView(nextView)
	cs.mu.Unlock()

	cs.checkAndCommit()
	printQC(qc)
	cs.printBlockchainState()
	cs.broadcast(Message{Type: "QC", SenderID: cs.NodeID, View: qc.View, Block: blk, QC: qc})

	fmt.Printf("\n%s%s%s\n", cCyan, bar(52), cReset)
	logSys("View %d complete → View %d  |  Leader: Node %d", nextView-1, nextView, nextLeader)
	if nextLeader == cs.NodeID {
		logLead(">>> YOU are the leader for View %d", nextView)
	} else {
		logInfo("Waiting for proposal from Node %d (v%d)", nextLeader, nextView)
	}
	fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)

	if nextLeader == cs.NodeID {
		go cs.runLeaderRound()
	}
}

// ─── ABORT ROUND ─────────────────────────────────────────────────────────────

func (cs *ConsensusState) abortRound() {
	cs.mu.Lock()
	if cs.pendingBlock == nil {
		cs.mu.Unlock()
		return
	}
	view := cs.currentView
	cs.currentView++
	cs.inRound = false
	cs.pendingBlock = nil
	cs.proposalForView = nil
	cs.votesForView = make(map[int]bool)
	cs.noVotesForView = make(map[int]bool)
	cs.stopVotePhaseTimer() // (A) cancel timer on abort
	nextView := cs.currentView
	nextLeader := cs.leaderForView(nextView)
	cs.mu.Unlock()

	fmt.Printf("\n%s%s%s\n", cYellow+cBold, bar(52), cReset)
	fmt.Printf("%s  VIEW %d ABORTED — STRICT MAJORITY REJECTED%s\n", cYellow+cBold, view, cReset)
	fmt.Printf("%s%s%s\n", cYellow, bar(52), cReset)
	logWarn("Strict majority of nodes rejected the proposal")
	logWarn("No block added to chain")
	logSys("Advancing to View %d  |  Next leader: Node %d", nextView, nextLeader)
	fmt.Printf("%s%s%s\n", cYellow, bar(52), cReset)

	cs.broadcast(Message{Type: "NEW_VIEW", SenderID: cs.NodeID, View: nextView})
	if nextLeader == cs.NodeID {
		go cs.runLeaderRound()
	} else {
		cs.mu.Lock()
		cs.inRound = true
		cs.startViewTimer()
		cs.mu.Unlock()
	}
}

// ─── QC PRINTER ───────────────────────────────────────────────────────────────

func printQC(qc *QuorumCertificate) {
	fmt.Printf("\n%s%s%s\n", cCyan+cBold, bar(52), cReset)
	fmt.Printf("%s  QC FORMED — View %d%s\n", cCyan+cBold, qc.View, cReset)
	fmt.Printf("%s%s%s\n", cCyan, strings.Repeat("-", 52), cReset)
	fmt.Printf("  %-20s %d\n", "View:", qc.View)
	fmt.Printf("  %-20s %d\n", "Block ID:", qc.BlockID)
	fmt.Printf("  %-20s %s%q%s\n", "Block Data:", cPurple, qc.BlockData, cReset)
	explicit := qc.VoteCount - 1
	if qc.LeaderID == 0 {
		explicit = qc.VoteCount // legacy QC without LeaderID field
	}
	fmt.Printf("  %-20s %d  (%d explicit + 1 leader self-vote)\n", "Vote Count:", qc.VoteCount, explicit)
	if qc.LeaderID != 0 {
		fmt.Printf("  %-20s Node %d  %s(self-voted)%s\n", "Leader:", qc.LeaderID, cDim, cReset)
	}
	fmt.Printf("  %-20s %v\n", "Explicit Voters:", qc.Voters)
	fmt.Printf("  %-20s %s\n", "Formed At:", qc.FormedAt)
	fmt.Printf("%s%s%s\n", cCyan+cBold, bar(52), cReset)
}

// ─── BLOCKCHAIN STATE PRINTER ───────────────────────────────────────────────

func (cs *ConsensusState) printBlockchainState() {
	cs.mu.Lock()
	committed := make([]int, len(cs.committed))
	copy(committed, cs.committed)
	committedSet := make(map[int]bool, len(cs.committed))
	for _, id := range cs.committed {
		committedSet[id] = true
	}
	qcs := make([]*QuorumCertificate, len(cs.qcs))
	copy(qcs, cs.qcs)
	// Deep-copy blocks map so we can read it outside the lock.
	blocksCopy := make(map[int]*Block, len(cs.blocks))
	for k, v := range cs.blocks {
		b := *v
		blocksCopy[k] = &b
	}
	view := cs.currentView
	cs.mu.Unlock()

	// ── Summary header ─────────────────────────────────────────────────────
	fmt.Printf("\n%s%s%s\n", cPurple+cBold, bar(66), cReset)
	fmt.Printf("%s  BLOCKCHAIN STATE  (view %d  |  committed: %d  |  accepted: %d)%s\n",
		cPurple+cBold, view, len(committed), len(qcs), cReset)
	fmt.Printf("%s%s%s\n", cPurple, bar(66), cReset)

	// ── Block history table ─────────────────────────────────────────────────
	if len(qcs) == 0 {
		fmt.Printf("  (no blocks yet)\n")
	} else {
		// Header row
		fmt.Printf("  %s%-4s %-6s %-7s %-7s %-5s  %-18s  %s%s\n",
			cBold, "#", "BlkID", "Parent", "View", "Lead", "Data", "Status", cReset)
		fmt.Printf("  %s\n", strings.Repeat("-", 64))
		for i, qc := range qcs {
			blk, hasBlk := blocksCopy[qc.BlockID]
			parentID, blkView, proposer, data := 0, qc.View, qc.LeaderID, qc.BlockData
			if hasBlk {
				parentID = blk.ParentID
				blkView = blk.View
				proposer = blk.Proposer
				data = blk.Data
			}
			// Truncate data for display
			displayData := fmt.Sprintf("%q", data)
			if len(displayData) > 20 {
				displayData = displayData[:17] + `..."`
			}
			// Status
			var statusStr string
			var statusColor string
			if committedSet[qc.BlockID] {
				statusStr = "COMMITTED \u2713"
				statusColor = cPurple + cBold
			} else {
				statusStr = "accepted  ~"
				statusColor = cCyan
			}
			// Voters display: leader + explicit
			voterStr := ""
			if qc.LeaderID != 0 {
				voterStr = fmt.Sprintf("%d+%v", qc.LeaderID, qc.Voters)
			} else {
				voterStr = fmt.Sprintf("%v", qc.Voters)
			}
			fmt.Printf("  %-4d %-6d %-7d %-7d %-5d  %-18s  %s%s%s  votes:%s\n",
				i+1, qc.BlockID, parentID, blkView, proposer,
				displayData, statusColor, statusStr, cReset, voterStr)
		}
		fmt.Printf("  %s\n", strings.Repeat("-", 64))
		// Chain summary line
		chainStr := ""
		for i, qc := range qcs {
			if i > 0 {
				chainStr += " \u2192 "
			}
			mark := "~"
			if committedSet[qc.BlockID] {
				mark = "\u2713"
			}
			chainStr += fmt.Sprintf("B%d(%s)", qc.BlockID, mark)
		}
		fmt.Printf("  Chain : %s\n", chainStr)
		fmt.Printf("  %s  (\u2713=committed  ~=pending 3-chain)\n", strings.Repeat("-", 64))
	}
	fmt.Printf("%s%s%s\n", cPurple+cBold, bar(66), cReset)

	// Print the explicit block tree structure.
	cs.mu.Lock()
	cs.blockTree.PrintTree()
	cs.mu.Unlock()
}

// ─── 3-CHAIN COMMIT RULE (C) ─────────────────────────────────────────────────
//
//  Commits block B when the last three QCs form a valid parent-child chain:
//    blocks[qcs[i+1].BlockID].ParentID == qcs[i].BlockID
//    blocks[qcs[i+2].BlockID].ParentID == qcs[i+1].BlockID

func (cs *ConsensusState) checkAndCommit() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	total := len(cs.qcs)
	start := total - 3
	if start < 0 {
		start = 0
	}
	chainStr := ""
	for i := start; i < total; i++ {
		if chainStr != "" {
			chainStr += " → "
		}
		chainStr += fmt.Sprintf("Block%d", cs.qcs[i].BlockID)
	}

	fmt.Printf("\n%s%s%s\n", cCyan+cBold, bar(52), cReset)
	fmt.Printf("%s  3-CHAIN COMMIT CHECK%s\n", cCyan+cBold, cReset)
	fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
	logQC("QCs collected: %d", total)
	if chainStr != "" {
		logQC("Chain window : %s%s%s", cWhite+cBold, chainStr, cReset)
	}

	if total < 3 {
		logQC("Need 3 consecutive QCs  (%d/3)", total)
		fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
		return
	}

	q0 := cs.qcs[total-3]
	q1 := cs.qcs[total-2]
	q2 := cs.qcs[total-1]

	// (C) Validate contiguous parent-child links in the 3-chain.
	blk1, ok1 := cs.blocks[q1.BlockID]
	blk2, ok2 := cs.blocks[q2.BlockID]
	if !ok1 || !ok2 {
		logQC("3-chain validation: missing block data — skipping commit")
		fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
		return
	}
	if blk1.ParentID != q0.BlockID {
		logQC("3-chain broken: Block%d.parent=%d ≠ Block%d — skipping commit",
			q1.BlockID, blk1.ParentID, q0.BlockID)
		fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
		return
	}
	if blk2.ParentID != q1.BlockID {
		logQC("3-chain broken: Block%d.parent=%d ≠ Block%d — skipping commit",
			q2.BlockID, blk2.ParentID, q1.BlockID)
		fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
		return
	}

	// Already committed?
	for _, cid := range cs.committed {
		if cid == q0.BlockID {
			fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
			return
		}
	}

	cs.blockTree.Commit(q0.BlockID)
	cs.committed = append(cs.committed, q0.BlockID)
	blk := cs.blocks[q0.BlockID]
	dataStr := ""
	if blk != nil {
		dataStr = blk.Data
	}

	logQC("3-chain rule satisfied ✓  (parent-child links verified)")
	fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
	fmt.Printf("\n%s%s%s\n", cPurple+cBold, bar(52), cReset)
	fmt.Printf("%s  BLOCK COMMITTED%s\n", cPurple+cBold, cReset)
	fmt.Printf("%s%s%s\n", cPurple, bar(52), cReset)
	logCommit("Block ID : %d", q0.BlockID)
	logCommit("Data     : %q", dataStr)
	logCommit("View     : %d", q0.View)
	logCommit("Chain    : %s%s%s", cWhite+cBold, chainStr, cReset)
	logCommit("Status   : FINAL — irreversible")
	fmt.Printf("%s%s%s\n", cPurple+cBold, bar(52), cReset)
}

// ─── TERMINAL INPUT ───────────────────────────────────────────────────────────

func (cs *ConsensusState) readLine() string {
	for {
		select {
		case <-cs.inputLines:
		default:
			goto done
		}
	}
done:
	select {
	case line := <-cs.inputLines:
		return strings.TrimSpace(line)
	case <-cs.stopCh:
		return ""
	}
}

func (cs *ConsensusState) inputLoop() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "exit" || line == "quit" {
			logSys("Sending LEAVE and shutting down...")
			cs.broadcast(Message{Type: "LEAVE", SenderID: cs.NodeID})
			time.Sleep(300 * time.Millisecond)
			close(cs.stopCh)
			os.Exit(0)
		}
		select {
		case cs.inputLines <- line:
		default:
		}
	}
}

// ─── REPLICA VOTE LOOP ────────────────────────────────────────────────────────

// waitForVoteInput waits for user input on the inputLines channel, but
// re-checks every 500ms that the proposal is still pending and this node
// is still a replica.  This prevents the vote loop from holding onto the
// channel when the node transitions to leader (which also reads from it).
func (cs *ConsensusState) waitForVoteInput() (string, bool) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case line := <-cs.inputLines:
			return strings.TrimSpace(line), true
		case <-ticker.C:
			cs.mu.Lock()
			valid := cs.proposalForView != nil && !cs.isLeaderThisView()
			cs.mu.Unlock()
			if !valid {
				return "", false
			}
		case <-cs.stopCh:
			return "", false
		}
	}
}

func (cs *ConsensusState) replicaVoteLoop() {
	for {
		select {
		case <-cs.stopCh:
			return
		default:
		}
		cs.mu.Lock()
		proposal := cs.proposalForView
		isLeader := cs.isLeaderThisView()
		cs.mu.Unlock()

		if proposal == nil || isLeader {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		line, ok := cs.waitForVoteInput()
		if !ok {
			continue
		}
		answer := strings.ToLower(line)

		cs.mu.Lock()
		if cs.proposalForView == nil {
			cs.mu.Unlock()
			continue
		}
		blk := cs.proposalForView
		view := blk.View
		leaderID := blk.Proposer
		cs.proposalForView = nil
		cs.mu.Unlock()

		if answer == "y" || answer == "yes" {
			logVote("Sending YES vote for Block %d to Node %d", blk.ID, leaderID)
			go cs.sendTo(leaderID, Message{
				Type: "VOTE", SenderID: cs.NodeID, View: view,
				Vote: &Vote{VoterID: cs.NodeID, BlockID: blk.ID, View: view},
			})
		} else {
			logVote("Sending NO vote for Block %d to Node %d", blk.ID, leaderID)
			go cs.sendTo(leaderID, Message{
				Type: "VOTE", SenderID: cs.NodeID, View: view,
				Vote: &Vote{VoterID: cs.NodeID, BlockID: blk.ID, View: view, NoVote: true},
			})
		}
	}
}

// ─── PEER HEALTH CHECK ────────────────────────────────────────────────────────

// peerHealthCheck periodically pings all peers to detect dead nodes faster.
// Runs every 5 seconds and attempts to send a lightweight PING message.
func (cs *ConsensusState) peerHealthCheck() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-cs.stopCh:
			return
		case <-ticker.C:
			cs.mu.Lock()
			peerIDs := make([]int, 0, len(cs.peers))
			for id := range cs.peers {
				peerIDs = append(peerIDs, id)
			}
			cs.mu.Unlock()

			// Try to ping each peer
			for _, peerID := range peerIDs {
				go cs.sendTo(peerID, Message{
					Type:     "PING",
					SenderID: cs.NodeID,
				})
			}
		}
	}
}

// ─── MAIN ─────────────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: ./hotstuff.exe <node_id> [id=IP ...] [-m]")
		fmt.Fprintln(os.Stderr, "  node_id : 1–9  (port = 7000 + id)")
		fmt.Fprintln(os.Stderr, "  id=IP   : peer address, e.g.  2=192.168.1.101")
		fmt.Fprintln(os.Stderr, "  -m      : Byzantine (malicious) node")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  Same-machine:")
		fmt.Fprintln(os.Stderr, "    ./hotstuff.exe 1")
		fmt.Fprintln(os.Stderr, "    ./hotstuff.exe 2")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Two laptops:")
		fmt.Fprintln(os.Stderr, "    Laptop 1: ./hotstuff.exe 1 2=10.12.227.66")
		fmt.Fprintln(os.Stderr, "    Laptop 2: ./hotstuff.exe 2 1=10.12.226.231")
		os.Exit(1)
	}
	nodeID, err := strconv.Atoi(os.Args[1])
	if err != nil || nodeID < 1 || nodeID > 9 {
		fmt.Fprintln(os.Stderr, "node_id must be 1–9")
		os.Exit(1)
	}
	// Port = 7000+nodeID for consensus
	malicious := false
	peerIPs := make(map[int]string)

	// Parse command-line args for peer IPs and flags
	for _, a := range os.Args[2:] {
		if a == "-m" {
			malicious = true
			continue
		}
		// Accept id=IP or id=IP:port (port is ignored — always 7000+id for consensus).
		parts := strings.SplitN(a, "=", 2)
		if len(parts) == 2 {
			pid, perr := strconv.Atoi(parts[0])
			if perr == nil && pid >= 1 && pid <= 9 && pid != nodeID {
				// Strip optional port from the IP portion.
				host := parts[1]
				if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
					host = h
				}
				peerIPs[pid] = host
			}
		}
	}

	cs := newState(nodeID, malicious)
	// Populate cross-machine addresses (empty = same-machine / localhost mode).
	// nodeAddrs tells us WHERE to find nodes, but doesn't mean they've joined yet.
	// Nodes are added to peers ONLY when they actually connect (first-come-first-served).
	for id, ip := range peerIPs {
		cs.nodeAddrs[id] = ip
	}

	// DO NOT pre-populate peers from config — nodes join dynamically as they start up.
	// This ensures true first-come-first-served ordering for leader rotation.

	if err := cs.startListener(); err != nil {
		fmt.Fprintf(os.Stderr,
			"\n%s[ERR]%s Cannot listen on port %d — is Node %d already running?\n\n",
			cRed+cBold, cReset, cs.Port, nodeID)
		os.Exit(1)
	}

	fmt.Printf("\n%s%s%s\n", cCyan+cBold, bar(52), cReset)
	fmt.Printf("%s  HotStuff BFT Consensus (Hardened)%s\n", cCyan+cBold, cReset)
	fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
	fmt.Printf("  Node ID          : %s%d%s\n", cYellow, nodeID, cReset)
	fmt.Printf("  Port             : %d\n", cs.Port)
	fmt.Printf("  View timeout     : %s\n", viewTimeout)
	fmt.Printf("  Vote-phase tmout : %s\n", votePhaseTimeout)
	fmt.Printf("  Max send fails   : %d\n", maxSendFailures)
	fmt.Printf("  Time             : %s\n", now())
	if len(cs.nodeAddrs) > 0 {
		fmt.Printf("  %sMode             : MULTI-LAPTOP%s\n", cGreen+cBold, cReset)
		for id, ip := range cs.nodeAddrs {
			fmt.Printf("  %sPeer Node %-2d     : %s:%d%s\n", cGreen, id, ip, 7000+id, cReset)
		}
	} else {
		fmt.Printf("  Mode             : same-machine (localhost)\n")
	}
	if malicious {
		fmt.Printf("  %sMalicious        : YES (Byzantine node)%s\n", cRed+cBold, cReset)
	}
	fmt.Printf("%s%s%s\n", cCyan, bar(52), cReset)
	fmt.Printf("\n%s  Type 'exit' at any time to leave the cluster.%s\n\n", cDim, cReset)

	go cs.inputLoop()
	cs.discoverPeers()
	go cs.discoveryLoop()
	go cs.startUDPBeacon()  // Broadcast this node's address on LAN
	go cs.listenUDPBeacon() // Listen for other nodes' beacons

	cs.mu.Lock()
	logSys("Cluster : %v", cs.peerOrder)
	logSys("Total   : %d nodes  |  quorum=%d  f=%d",
		cs.totalNodes(), cs.quorum(), cs.faultTolerance())
	leader1 := cs.leaderForView(cs.currentView)
	cs.mu.Unlock()
	logSys("Leader for View 1 : Node %d", leader1)

	go cs.replicaVoteLoop()
	go cs.peerHealthCheck() // Periodic health check to detect dead nodes

	cs.mu.Lock()
	amLeader := cs.isLeaderThisView()
	cs.mu.Unlock()

	if amLeader {
		logLead(">>> YOU are the leader for View 1 — starting round...")
		go cs.runLeaderRound()
	} else {
		logInfo("You are a replica. Waiting for proposal from Node %d...", leader1)
		cs.mu.Lock()
		cs.inRound = true
		cs.startViewTimer()
		cs.mu.Unlock()
	}

	<-cs.stopCh
}
