package main

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

const (
	viewTimeout      = 20 * time.Second
	votePhaseTimeout = 12 * time.Second
	maxSendFailures  = 30
)

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

type Block struct {
	ID       int    `json:"id"`
	ParentID int    `json:"parent_id"`
	View     int    `json:"view"`
	Proposer int    `json:"proposer"`
	Data     string `json:"data"`
	Checksum string `json:"checksum"`
}

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
	VoteCount int    `json:"vote_count"`
	LeaderID  int    `json:"leader_id"`
	Voters    []int  `json:"voters"`
	FormedAt  string `json:"formed_at"`
}

type Message struct {
	Type           string             `json:"type"`
	SenderID       int                `json:"sender_id"`
	SenderPort     int                `json:"sender_port"`
	StartOrderKey  int64              `json:"start_order_key,omitempty"`
	StartOrderMap  map[int]int64      `json:"start_order_map,omitempty"` // full order map shared on handshake
	View           int                `json:"view"`
	Block          *Block             `json:"block,omitempty"`
	Vote           *Vote              `json:"vote,omitempty"`
	QC             *QuorumCertificate `json:"qc,omitempty"`
	KnownPeers     []int              `json:"known_peers,omitempty"`
	HighestQCBlock int                `json:"highest_qc_block,omitempty"`
	NodeAddrsMap   map[int]string     `json:"node_addrs_map,omitempty"`
}

type ConsensusState struct {
	mu sync.Mutex

	NodeID    int
	Port      int
	malicious bool

	nodeAddrs map[int]string

	peers     map[int]string
	peerOrder []int
	// startTime[id] = UnixNano when that node booted.
	// Smaller value = started earlier = earlier in leader rotation.
	// Shared via HELLO/HELLO_ACK so every node converges on the same order.
	startTime    map[int]int64
	myStartTime  int64 // this node's own boot time, never overwritten

	sendFailures  map[int]int
	failureLastAt map[int]time.Time
	helloSentAt   map[int]time.Time

	currentView int
	inRound     bool

	nextBlockID    int
	blocks         map[int]*Block
	qcs            []*QuorumCertificate
	committed      []int
	highestQCBlock int
	blockTree      *BlockTree

	pendingBlock   *Block
	votesForView   map[int]bool
	noVotesForView map[int]bool

	proposalForView *Block

	viewTimer      *time.Timer
	votePhaseTimer *time.Timer

	listener   net.Listener
	stopCh     chan struct{}
	inputLines chan string
	proposalCh chan *Block // signals replicaVoteLoop that a new proposal arrived
}

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
		cs.drainProposalCh()
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

func newState(nodeID int, malicious bool) *ConsensusState {
	bootTime := time.Now().UnixNano()
	return &ConsensusState{
		NodeID:         nodeID,
		Port:           7000 + nodeID,
		malicious:      malicious,
		nodeAddrs:      make(map[int]string),
		peers:          make(map[int]string),
		peerOrder:      []int{nodeID},
		startTime:      map[int]int64{nodeID: bootTime},
		myStartTime:    bootTime,
		sendFailures:   make(map[int]int),
		failureLastAt:  make(map[int]time.Time),
		helloSentAt:    make(map[int]time.Time),
		currentView:    1,
		nextBlockID:    1,
		blocks:         make(map[int]*Block),
		qcs:            []*QuorumCertificate{},
		committed:      []int{},
		highestQCBlock: 0,
		blockTree:      NewBlockTree(),
		votesForView:   make(map[int]bool),
		noVotesForView: make(map[int]bool),
		stopCh:         make(chan struct{}),
		inputLines:     make(chan string, 64),
		proposalCh:     make(chan *Block, 4),
	}
}

// noteFirstSeen records a peer's boot time only if we don't know it yet.
// The actual boot time arrives via StartOrderMap in HELLO/HELLO_ACK.
// This is a fallback that assigns current time (will be overridden by merge).
func (cs *ConsensusState) noteFirstSeen(id int) {
	if id <= 0 {
		return
	}
	if _, ok := cs.startTime[id]; ok {
		return // already have real boot time
	}
	// We don't know their real boot time yet; use current time as placeholder.
	// It will be overwritten when we receive their StartOrderMap.
	cs.startTime[id] = time.Now().UnixNano()
}

// mergeStartTimes absorbs a remote node's boot-time map.
// For each peer, keep whichever boot time is EARLIER (smaller UnixNano).
// Never overwrite our own boot time.
func (cs *ConsensusState) mergeStartTimes(remote map[int]int64) {
	for id, remoteTime := range remote {
		if id == cs.NodeID {
			continue // never overwrite our own boot time
		}
		if remoteTime <= 0 {
			continue
		}
		if local, ok := cs.startTime[id]; !ok || remoteTime < local {
			cs.startTime[id] = remoteTime
		}
	}
}

func (cs *ConsensusState) rebuildOrder() {
	// Build peerOrder sorted by boot time (UnixNano, smaller = earlier = first leader).
	// Tie-break on node ID for same-machine runs where clocks are identical.
	// Every node converges on the same order because boot times are shared and merged.
	ids := make([]int, 0, 1+len(cs.peers))
	ids = append(ids, cs.NodeID)
	for id := range cs.peers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		ti := cs.startTime[ids[i]]
		tj := cs.startTime[ids[j]]
		if ti != tj {
			return ti < tj
		}
		return ids[i] < ids[j]
	})
	cs.peerOrder = ids
}

func (cs *ConsensusState) totalNodes() int { return len(cs.peerOrder) }

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

	if q < 2 {
		q = 2
	}
	return q
}

func (cs *ConsensusState) faultTolerance() int { return (cs.totalNodes() - 1) / 3 }

func (cs *ConsensusState) isLeaderThisView() bool {
	return cs.leaderForView(cs.currentView) == cs.NodeID
}

func (cs *ConsensusState) handleMessage(msg Message) {
	switch msg.Type {

	case "HELLO":
		cs.mu.Lock()
		// Merge boot times first so noteFirstSeen placeholder is overridden immediately
		if len(msg.StartOrderMap) > 0 {
			cs.mergeStartTimes(msg.StartOrderMap)
		}
		cs.noteFirstSeen(msg.SenderID)
		isNew := false
		if _, exists := cs.peers[msg.SenderID]; !exists {
			host := "localhost"
			if configured, ok := cs.nodeAddrs[msg.SenderID]; ok && configured != "" {
				host = configured
			}
			cs.peers[msg.SenderID] = fmt.Sprintf("%s:%d", host, 7000+msg.SenderID)
			isNew = true
		}
		cs.rebuildOrder()
		orderSnap := make([]int, len(cs.peerOrder))
		copy(orderSnap, cs.peerOrder)
		view := cs.currentView
		hqcBlock := cs.highestQCBlock
		addrsCopy := make(map[int]string, len(cs.nodeAddrs))
		for id, ip := range cs.nodeAddrs {
			addrsCopy[id] = ip
		}
		// Share our full startTime map so the sender can merge and agree on the same order
		timeMapCopy := make(map[int]int64, len(cs.startTime))
		for id, t := range cs.startTime {
			timeMapCopy[id] = t
		}
		cs.mu.Unlock()

		if isNew {
			fmt.Printf("\n%s%s%s\n", cGreen+cBold, bar(52), cReset)
			logNet("Node %d joined the network", msg.SenderID)
			cs.mu.Lock()
			logSys("Cluster (first-seen): %v  total=%d  quorum=%d  f=%d",
				cs.peerOrder, cs.totalNodes(), cs.quorum(), cs.faultTolerance())
			cs.mu.Unlock()
			fmt.Printf("%s%s%s\n", cGreen, bar(52), cReset)
			cs.dashEvent("NET", fmt.Sprintf("Node %d joined the cluster", msg.SenderID))
			go cs.dashPushState()
		}

		go cs.sendTo(msg.SenderID, Message{
			Type:           "HELLO_ACK",
			SenderID:       cs.NodeID,
			StartOrderKey:  cs.myStartTime,
			StartOrderMap:  timeMapCopy,
			View:           view,
			KnownPeers:     orderSnap,
			HighestQCBlock: hqcBlock,
			NodeAddrsMap:   addrsCopy,
		})

	case "HELLO_ACK":
		cs.mu.Lock()
		// Merge boot times FIRST so noteFirstSeen placeholder is overridden
		if len(msg.StartOrderMap) > 0 {
			cs.mergeStartTimes(msg.StartOrderMap)
		} else if msg.StartOrderKey > 0 {
			// Fallback: remote only sent StartOrderKey (their boot time)
			if local, ok := cs.startTime[msg.SenderID]; !ok || msg.StartOrderKey < local {
				cs.startTime[msg.SenderID] = msg.StartOrderKey
			}
		}
		cs.noteFirstSeen(msg.SenderID)
		isNew := false
		if msg.SenderID > 0 {
			if _, exists := cs.peers[msg.SenderID]; !exists {
				host := "localhost"
				if configured, ok := cs.nodeAddrs[msg.SenderID]; ok && configured != "" {
					host = configured
				}
				cs.peers[msg.SenderID] = fmt.Sprintf("%s:%d", host, 7000+msg.SenderID)
				isNew = true
			}
		}

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

		// KnownPeers: register any peers we haven't heard of yet
		for _, id := range msg.KnownPeers {
			if id != cs.NodeID {
				cs.noteFirstSeen(id)
				if _, exists := cs.peers[id]; !exists {
					host := "localhost"
					if configured, ok := cs.nodeAddrs[id]; ok && configured != "" {
						host = configured
					}
					cs.peers[id] = fmt.Sprintf("%s:%d", host, 7000+id)
				}
			}
		}

		cs.rebuildOrder()

		if msg.View > cs.currentView {
			cs.currentView = msg.View
		}
		if msg.HighestQCBlock > cs.highestQCBlock {
			cs.highestQCBlock = msg.HighestQCBlock
		}
		cs.mu.Unlock()

		if isNew {
			fmt.Printf("\n%s%s%s\n", cGreen+cBold, bar(52), cReset)
			logNet("Node %d acknowledged (HELLO_ACK)", msg.SenderID)
			cs.mu.Lock()
			logSys("Cluster (first-seen): %v  total=%d  quorum=%d  f=%d",
				cs.peerOrder, cs.totalNodes(), cs.quorum(), cs.faultTolerance())
			cs.mu.Unlock()
			fmt.Printf("%s%s%s\n", cGreen, bar(52), cReset)
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

		checksumOK := blk.Checksum == "" || blk.Checksum == computeChecksum(blk.Data)
		if !checksumOK {
			expected := computeChecksum(blk.Data)
			cs.mu.Unlock()
			fmt.Printf("\n%s%s%s\n", cRed+cBold, bar(52), cReset)
			logWarn("[CHECKSUM FAIL] Block %d from Node %d — expected %s, got %s",
				blk.ID, msg.SenderID, expected, blk.Checksum)
			logWarn("AUTO-REJECTING proposal (Byzantine node detected)")
			cs.dashEvent("WARN", fmt.Sprintf("[CHECKSUM FAIL] Block %d from Node %d (view %d) — expected %s, got %s",
				blk.ID, msg.SenderID, msg.View, expected, blk.Checksum))
			cs.dashEvent("WARN", "AUTO-REJECTING proposal (Byzantine node detected)")
			fmt.Printf("%s%s%s\n", cRed, bar(52), cReset)
			go cs.sendTo(msg.SenderID, Message{
				Type:     "VOTE",
				SenderID: cs.NodeID,
				View:     msg.View,
				Vote:     &Vote{VoterID: cs.NodeID, BlockID: blk.ID, View: msg.View, NoVote: true},
			})
			return
		}

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
		leaderOfProposal := msg.SenderID
		cs.mu.Unlock()
		cs.printProposalAndPrompt(blk, leaderOfProposal, msg.View)
		// Signal the vote loop that a fresh proposal is ready
		select {
		case cs.proposalCh <- blk:
		default:
		}

	case "VOTE":
		if msg.Vote == nil {
			return
		}
		cs.mu.Lock()

		if !cs.isLeaderThisView() || msg.View != cs.currentView || cs.pendingBlock == nil {
			cs.mu.Unlock()
			return
		}

		if msg.SenderID != msg.Vote.VoterID {
			cs.mu.Unlock()
			logWarn("VOTE rejected: SenderID %d != VoterID %d (spoofed?)", msg.SenderID, msg.Vote.VoterID)
			return
		}

		if msg.Vote.BlockID != cs.pendingBlock.ID {
			cs.mu.Unlock()
			logWarn("VOTE rejected: blockID %d != pendingBlock %d", msg.Vote.BlockID, cs.pendingBlock.ID)
			return
		}
		voterID := msg.Vote.VoterID
		total := cs.totalNodes()

		if msg.Vote.NoVote {
			if _, already := cs.noVotesForView[voterID]; already {
				cs.mu.Unlock()
				return
			}
			cs.noVotesForView[voterID] = true
			noCount := len(cs.noVotesForView)
			// Replicas = total - 1 (leader doesn't send NO to itself)
			replicas := total - 1
			cs.mu.Unlock()
			logVote("NO vote from Node %d  |  NO: %d/%d replicas", voterID, noCount, replicas)

			// Abort immediately if ALL replicas voted NO (unanimous rejection)
			// OR if strict majority (> half of ALL nodes) voted NO
			if noCount >= replicas || noCount > total/2 {
				logWarn("All replicas rejected — aborting round immediately")
				cs.abortRound()
			}
			return
		}

		if _, already := cs.votesForView[voterID]; already {
			cs.mu.Unlock()
			return
		}
		cs.votesForView[voterID] = true
		yesVotes := len(cs.votesForView) // leader already counted via explicit self-vote
		needed := cs.quorum()
		cs.mu.Unlock()
		logVote("YES vote from Node %d  |  YES: %d/%d", voterID, yesVotes, needed)
		cs.dashEvent("VOTE", fmt.Sprintf("YES vote from Node %d  (%d/%d)", voterID, yesVotes, needed))

		if yesVotes >= needed {
			cs.formAndBroadcastQC()
		}

	case "QC":
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
			// Drain any stale proposal so replicaVoteLoop doesn't vote on old block
			cs.drainProposalCh()
			logWarn("NEW_VIEW from Node %d → view %d  |  leader: Node %d",
				msg.SenderID, msg.View, leader)
			cs.dashEvent("WARN", fmt.Sprintf("View change → %d (leader: Node %d)", msg.View, leader))
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

	case "LEAVE":
		cs.removePeer(msg.SenderID, true)
	}
}

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
	fmt.Printf("\n%s✓ Checksum verified — Accept this proposal? (y/n, timeout=%s): %s\n> ",
		cGreen+cBold, votePhaseTimeout, cReset)
	go cs.dashVotePrompt(blk, leaderID, view)
	cs.dashEvent("VOTE", fmt.Sprintf("Proposal received — Block %d from Node %d (view %d)", blk.ID, leaderID, view))
}

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

	cs.broadcast(Message{Type: "PREPARE", SenderID: cs.NodeID, View: view})

	// Drain any stale input that accumulated while waiting.
drainLeader:
	for {
		select {
		case <-cs.inputLines:
		default:
			break drainLeader
		}
	}

	fmt.Printf("%s  Enter block data (timeout in %s → view change): %s\n> ", cYellow+cBold, votePhaseTimeout, cReset)

	// Wait for input with a timeout. If leader doesn't type in time → view change.
	var data string
	inputTimeout := time.NewTimer(votePhaseTimeout)
	defer inputTimeout.Stop()
	inputDone := false
	for !inputDone {
		select {
		case <-cs.stopCh:
			return
		case line := <-cs.inputLines:
			data = strings.TrimSpace(line)
			inputDone = true
		case <-inputTimeout.C:
			// Leader didn't enter data in time — trigger view change
			cs.mu.Lock()
			if cs.currentView != view || !cs.isLeaderThisView() {
				cs.mu.Unlock()
				return
			}
			newView := cs.currentView + 1
			cs.currentView = newView
			cs.inRound = false
			newLeader := cs.leaderForView(newView)
			cs.mu.Unlock()
			fmt.Printf("\n%s%s%s\n", cYellow+cBold, bar(52), cReset)
			logWarn("INPUT TIMEOUT — leader did not enter data in %s — VIEW CHANGE to view %d", votePhaseTimeout, newView)
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
			return
		}
	}

	if data == "" {
		// Empty enter also triggers view change — no auto-generation
		cs.mu.Lock()
		if cs.currentView != view || !cs.isLeaderThisView() {
			cs.mu.Unlock()
			return
		}
		newView := cs.currentView + 1
		cs.currentView = newView
		cs.inRound = false
		newLeader := cs.leaderForView(newView)
		cs.mu.Unlock()
		fmt.Printf("\n%s%s%s\n", cYellow+cBold, bar(52), cReset)
		logWarn("Empty input — no block proposed — VIEW CHANGE to view %d", newView)
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
		return
	}

	cs.mu.Lock()

	if cs.currentView != view || !cs.isLeaderThisView() {
		cs.mu.Unlock()
		logWarn("View changed while waiting for input — aborting proposal")
		return
	}
	parentID := cs.highestQCBlock
	blockID := cs.nextBlockID
	cs.nextBlockID++
	checksum := computeChecksum(data)
	if cs.malicious {

		checksum = fmt.Sprintf("%08x", rand.Uint32())
		logWarn("[BYZANTINE] Sending corrupted checksum %s (real=%s)", checksum, computeChecksum(data))
	}
	blk := &Block{ID: blockID, ParentID: parentID, View: view, Proposer: cs.NodeID, Data: data, Checksum: checksum}
	cs.pendingBlock = blk
	cs.blocks[blockID] = blk
	_ = cs.blockTree.AddBlock(blk)
	// Leader casts its own YES vote explicitly — this makes the count honest
	// and consistent with quorum() which counts all nodes including the leader.
	cs.votesForView[cs.NodeID] = true
	cs.mu.Unlock()

	logLead("Proposing Block %d (parent=%d)  data=%q  checksum=%s", blk.ID, blk.ParentID, blk.Data, blk.Checksum)
	fmt.Printf("%s%s%s\n", cYellow, bar(52), cReset)

	cs.broadcast(Message{Type: "PROPOSE", SenderID: cs.NodeID, View: view, Block: blk})

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

func (cs *ConsensusState) formAndBroadcastQC() {
	cs.mu.Lock()
	if cs.pendingBlock == nil {
		cs.mu.Unlock()
		return
	}
	blk := cs.pendingBlock
	voters := []int{}
	for id := range cs.votesForView {
		voters = append(voters, id)
	}
	sort.Ints(voters)
	qc := &QuorumCertificate{
		BlockID:   blk.ID,
		BlockData: blk.Data,
		View:      cs.currentView,
		VoteCount: len(voters), // leader's self-vote is already in voters
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
	cs.stopVotePhaseTimer()
	nextView := cs.currentView
	nextLeader := cs.leaderForView(nextView)
	cs.mu.Unlock()

	cs.checkAndCommit()
	printQC(qc)
	cs.printBlockchainState()
	cs.broadcast(Message{Type: "QC", SenderID: cs.NodeID, View: qc.View, Block: blk, QC: qc})
	cs.dashEvent("QC", fmt.Sprintf("QC formed for Block %d — view %d — %d votes", qc.BlockID, qc.View, qc.VoteCount))
	go cs.dashPushState()

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
	cs.stopVotePhaseTimer()
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
	cs.drainProposalCh()
	if nextLeader == cs.NodeID {
		go cs.runLeaderRound()
	} else {
		cs.mu.Lock()
		cs.inRound = true
		cs.startViewTimer()
		cs.mu.Unlock()
	}
}

func printQC(qc *QuorumCertificate) {
	fmt.Printf("\n%s%s%s\n", cCyan+cBold, bar(52), cReset)
	fmt.Printf("%s  QC FORMED — View %d%s\n", cCyan+cBold, qc.View, cReset)
	fmt.Printf("%s%s%s\n", cCyan, strings.Repeat("-", 52), cReset)
	fmt.Printf("  %-20s %d\n", "View:", qc.View)
	fmt.Printf("  %-20s %d\n", "Block ID:", qc.BlockID)
	fmt.Printf("  %-20s %s%q%s\n", "Block Data:", cPurple, qc.BlockData, cReset)
	fmt.Printf("  %-20s %d\n", "Vote Count:", qc.VoteCount)
	if qc.LeaderID != 0 {
		fmt.Printf("  %-20s Node %d\n", "Leader:", qc.LeaderID)
	}
	fmt.Printf("  %-20s %v\n", "Voters:", qc.Voters)
	fmt.Printf("  %-20s %s\n", "Formed At:", qc.FormedAt)
	fmt.Printf("%s%s%s\n", cCyan+cBold, bar(52), cReset)
}

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

	blocksCopy := make(map[int]*Block, len(cs.blocks))
	for k, v := range cs.blocks {
		b := *v
		blocksCopy[k] = &b
	}
	view := cs.currentView
	cs.mu.Unlock()

	fmt.Printf("\n%s%s%s\n", cPurple+cBold, bar(66), cReset)
	fmt.Printf("%s  BLOCKCHAIN STATE  (view %d  |  committed: %d  |  accepted: %d)%s\n",
		cPurple+cBold, view, len(committed), len(qcs), cReset)
	fmt.Printf("%s%s%s\n", cPurple, bar(66), cReset)

	if len(qcs) == 0 {
		fmt.Printf("  (no blocks yet)\n")
	} else {

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

			displayData := fmt.Sprintf("%q", data)
			if len(displayData) > 20 {
				displayData = displayData[:17] + `..."`
			}

			var statusStr string
			var statusColor string
			if committedSet[qc.BlockID] {
				statusStr = "COMMITTED \u2713"
				statusColor = cPurple + cBold
			} else {
				statusStr = "accepted  ~"
				statusColor = cCyan
			}

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

	cs.mu.Lock()
	cs.blockTree.PrintTree()
	cs.mu.Unlock()
}

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
	cs.dashEvent("COMMIT", fmt.Sprintf("Block %d committed — data: %q", q0.BlockID, dataStr))

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

func (cs *ConsensusState) readLine() string {
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

func (cs *ConsensusState) drainProposalCh() {
	for {
		select {
		case <-cs.proposalCh:
		default:
			return
		}
	}
}

func (cs *ConsensusState) replicaVoteLoop() {
	for {
		// Wait for a proposal to arrive via proposalCh
		var blk *Block
		select {
		case <-cs.stopCh:
			return
		case blk = <-cs.proposalCh:
		}

		// Sanity check — if we became leader while waiting, ignore
		cs.mu.Lock()
		isLeader := cs.isLeaderThisView()
		stillMine := cs.proposalForView != nil && cs.proposalForView.ID == blk.ID
		cs.mu.Unlock()
		if isLeader || !stillMine {
			continue
		}

		// Wait for user input (y/n), but abandon if view changes or timeout
		var answer string
		waitDone := false
		voteTimeout := time.NewTimer(votePhaseTimeout)
		defer voteTimeout.Stop()
		for !waitDone {
			select {
			case <-cs.stopCh:
				return
			case line := <-cs.inputLines:
				answer = strings.ToLower(strings.TrimSpace(line))
				if answer != "" {
					waitDone = true
				}
			case <-voteTimeout.C:
				logWarn("Vote timeout — no input received in %s — treating as NO vote", votePhaseTimeout)
				answer = "n"
				waitDone = true
			case <-time.After(300 * time.Millisecond):
				cs.mu.Lock()
				stillValid := cs.proposalForView != nil &&
					cs.proposalForView.ID == blk.ID &&
					!cs.isLeaderThisView()
				cs.mu.Unlock()
				if !stillValid {
					waitDone = true // view changed or proposal gone — abandon
				}
			}
		}

		if answer == "" {
			// Proposal was revoked (view changed) before user answered
			continue
		}

		// Re-check the proposal is still valid before sending vote
		cs.mu.Lock()
		if cs.proposalForView == nil || cs.proposalForView.ID != blk.ID {
			cs.mu.Unlock()
			continue // proposal already gone
		}
		view := blk.View
		leaderID := blk.Proposer
		cs.proposalForView = nil // clear only after we have committed to voting
		cs.mu.Unlock()

		if answer == "y" || answer == "yes" {
			logVote("Sending YES vote for Block %d to Node %d", blk.ID, leaderID)
			cs.dashEvent("VOTE", fmt.Sprintf("You voted YES for Block %d", blk.ID))
			go cs.sendTo(leaderID, Message{
				Type:     "VOTE",
				SenderID: cs.NodeID,
				View:     view,
				Vote:     &Vote{VoterID: cs.NodeID, BlockID: blk.ID, View: view},
			})
		} else {
			logVote("Sending NO vote for Block %d to Node %d", blk.ID, leaderID)
			cs.dashEvent("VOTE", fmt.Sprintf("You voted NO for Block %d", blk.ID))
			go cs.sendTo(leaderID, Message{
				Type:     "VOTE",
				SenderID: cs.NodeID,
				View:     view,
				Vote:     &Vote{VoterID: cs.NodeID, BlockID: blk.ID, View: view, NoVote: true},
			})
		}
	}
}

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

			for _, peerID := range peerIDs {
				go cs.sendTo(peerID, Message{
					Type:     "PING",
					SenderID: cs.NodeID,
				})
			}
		}
	}
}

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

	malicious := false
	peerIPs := make(map[int]string)

	for _, a := range os.Args[2:] {
		if a == "-m" {
			malicious = true
			continue
		}

		parts := strings.SplitN(a, "=", 2)
		if len(parts) == 2 {
			pid, perr := strconv.Atoi(parts[0])
			if perr == nil && pid >= 1 && pid <= 9 && pid != nodeID {

				host := parts[1]
				if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
					host = h
				}
				peerIPs[pid] = host
			}
		}
	}

	cs := newState(nodeID, malicious)

	for id, ip := range peerIPs {
		cs.nodeAddrs[id] = ip
	}

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
	cs.startDashboard()
	cs.discoverPeers()
	go cs.discoveryLoop()
	go cs.startUDPBeacon()
	go cs.listenUDPBeacon()

	cs.mu.Lock()
	logSys("Cluster (first-seen) : %v", cs.peerOrder)
	logSys("Total   : %d nodes  |  quorum=%d  f=%d",
		cs.totalNodes(), cs.quorum(), cs.faultTolerance())
	leader1 := cs.leaderForView(cs.currentView)
	cs.mu.Unlock()
	logSys("Leader for View 1 : Node %d", leader1)

	go cs.replicaVoteLoop()
	go cs.peerHealthCheck()

	if len(cs.nodeAddrs) > 0 {
		expectedPeers := len(cs.nodeAddrs) // number of peers configured on cmd line
		logInfo("Waiting for all %d configured peers to connect before starting consensus...", expectedPeers)

		// Phase 1: wait until ALL configured peers are connected, or 15 s timeout.
		// 15 s is generous — if a node never shows up we still start with whoever is here.
		deadline := time.Now().Add(15 * time.Second)
		for {
			select {
			case <-cs.stopCh:
				return
			default:
			}
			cs.mu.Lock()
			got := len(cs.peers)
			cs.mu.Unlock()
			if got >= expectedPeers {
				break
			}
			if time.Now().After(deadline) {
				cs.mu.Lock()
				got = len(cs.peers)
				cs.mu.Unlock()
				if got == 0 {
					logWarn("No peers connected after 15s — continuing alone (single-node mode)")
				} else {
					logWarn("Only %d/%d peers connected after 15s — starting with current cluster", got, expectedPeers)
				}
				break
			}
			time.Sleep(200 * time.Millisecond)
		}

		// Phase 2: give an extra 1 s for HELLO_ACK start-times to propagate
		// so rebuildOrder() has complete data for all connected peers.
		time.Sleep(1 * time.Second)

		cs.mu.Lock()
		cs.rebuildOrder() // final order with all start times known
		leader1 = cs.leaderForView(cs.currentView)
		logSys("Cluster (first-seen) : %v", cs.peerOrder)
		logSys("Total   : %d nodes  |  quorum=%d  f=%d",
			cs.totalNodes(), cs.quorum(), cs.faultTolerance())
		cs.mu.Unlock()
		logSys("Leader for View %d : Node %d", cs.currentView, leader1)
	}

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
