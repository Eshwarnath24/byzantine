# HotStuff BFT Consensus

## Overview

A fully working implementation of the **HotStuff Byzantine Fault Tolerant (BFT)** consensus protocol written in Go. Nodes communicate over TCP, automatically discover each other, and rotate leadership every view. Supports both single-machine (multiple terminals) and multi-laptop (LAN) deployment.

---

## Files

| File | Purpose |
|------|---------|
| `hotstuff.go` | Core consensus state machine, protocol logic, leader/replica roles |
| `network.go` | TCP transport — listener, send, broadcast, peer discovery |
| `tree.go` | Explicit block tree with parent-child links, locking, and commit pruning |
| `web.go` | HTTP + SSE web dashboard server; REST input endpoint |
| `dashboard.html` | Browser dashboard UI (embedded into binary at build time) |
| `go.mod` | Go module definition |

---

## Features

- **Interactive terminal voting** — replicas type `y`/`n` to approve or reject proposals
- **Browser-based web dashboard** — live view of cluster state, blockchain, and event log at `http://localhost:7000+NodeID`; supports voting and data entry directly from the browser
- **Byzantine fault tolerance** — tolerates `f = (n-1)/3` faulty nodes; run any node with `-m` to simulate a Byzantine attacker
- **3-chain commit rule** — a block is finalized only after 3 consecutive QC'd descendants
- **Leader rotation** — leadership cycles through all nodes in join-arrival order
- **Automatic peer discovery** — nodes scan localhost ports and configured IPs on startup
- **View-change timeouts** — view advances automatically if the leader is silent or quorum stalls
- **Crash detection** — peers are removed after 3 consecutive TCP failures
- **CRC32 checksum validation** — Byzantine nodes send a corrupted checksum; honest replicas auto-reject
- **Color-coded logging** — LEADER (yellow), VOTE (green), QC (cyan), COMMIT (purple), ERR (red)

---

## Consensus Parameters

| Parameter | Value |
|-----------|-------|
| Node IDs | 1 – 9 |
| Consensus port | `8000 + NodeID` (Node 1 → 8001, Node 4 → 8004) |
| Web dashboard port | `7000 + NodeID` (Node 1 → http://localhost:7001, Node 4 → http://localhost:7004) |
| Quorum | `2f + 1` |
| Fault tolerance | `f = (n-1)/3` |
| View timeout | 20 seconds |
| Vote-phase timeout | 12 seconds |
| Max send failures | 3 (then peer is removed) |

---

## How to Run

### Prerequisites

- Go 1.20 or later
- All machines on the same network (for multi-laptop mode)

---

### Option A — Same Machine (4 terminals)

Open 4 terminal windows in the `hs3` directory and run one command per terminal:

```
go run . 1
go run . 2
go run . 3
go run . 4
```

To make **Node 4 Byzantine** (malicious):
```
go run . 4 -m
```

---

### Option B — 2 Laptops (2 nodes each)

First find each laptop's IP:
```
ipconfig
```
Look for **IPv4 Address** under Wi-Fi or Ethernet.

**Open firewall on both laptops (run as Administrator):**
```
netsh advfirewall firewall add rule name="HotStuff" dir=in action=allow protocol=TCP localport=8001-8004
netsh advfirewall firewall add rule name="HotStuff-Web" dir=in action=allow protocol=TCP localport=7001-7004
```

**Your Laptop** (e.g. `10.12.76.144`) — 2 terminals:
```
go run . 1 3=<friend_IP> 4=<friend_IP>
go run . 2 3=<friend_IP> 4=<friend_IP>
```

**Friend's Laptop** (e.g. `10.12.77.238`) — 2 terminals:
```
go run . 3 1=<your_IP> 2=<your_IP>
go run . 4 1=<your_IP> 2=<your_IP> -m
```

> Node IDs on the same laptop discover each other via localhost automatically.  
> Only nodes on the other machine need an explicit `id=IP` argument.

---

### Option C — 4 Laptops (1 node each)

Replace IPs with actual values:

```
# Laptop 1 (IP: 192.168.1.1)
go run . 1 2=192.168.1.2 3=192.168.1.3 4=192.168.1.4

# Laptop 2 (IP: 192.168.1.2)
go run . 2 1=192.168.1.1 3=192.168.1.3 4=192.168.1.4

# Laptop 3 (IP: 192.168.1.3)
go run . 3 1=192.168.1.1 2=192.168.1.2 4=192.168.1.4

# Laptop 4 (IP: 192.168.1.4)
go run . 4 1=192.168.1.1 2=192.168.1.2 3=192.168.1.3
```

---

## Using the System

### As a Leader

When it is your turn to lead (indicated by `>>> YOU are the leader for View N`):

```
STEP 1 — PROPOSE PHASE  (View 1)
Enter block data: transaction-batch-1
```

Type any string and press Enter. If you leave it empty, a random value is auto-generated.

### As a Replica

When the leader broadcasts a proposal, you will see the full block details followed by:

```
VIEW 1 — PROPOSAL RECEIVED
  Leader:              Node 1
  Block ID:            1
  Parent Block ID:     0
  Block Data:          "transaction-batch-1"
  View:                1
  Checksum:            9f62570c ✓ VALID

Checksum OK — Accept proposal from leader? (y/n):
```

- Type `y` or `yes` → sends a **YES vote**
- Type `n` or `no` → sends a **NO vote**

### Leaving

Type `exit` at any prompt to broadcast a LEAVE message and shut down gracefully.

---

## Web Dashboard

Each node starts an HTTP server alongside the consensus port:

| Node | Dashboard URL |
|------|--------------|
| Node 1 | http://localhost:7001 |
| Node 2 | http://localhost:7002 |
| Node 3 | http://localhost:7003 |
| Node 4 | http://localhost:7004 |

Open the URL in a browser after starting the node. The dashboard provides:

- **Cluster panel** — live list of all nodes; highlights the current leader and marks your own node
- **Status bar** — current view number, leader, quorum size, committed block count, accepted block count
- **Blockchain view** — scrollable list of all QC'd blocks with commit status
- **Event log** — colour-coded real-time stream of LEADER / VOTE / QC / COMMIT / WARN / ERR events
- **Interactive prompts** — when it is your turn to propose (leader) or vote (replica), an input form appears in the browser so you can participate without touching the terminal
  - Leader prompt: type block data and click **Propose**
  - Replica prompt: click **Accept** or **Reject**

The dashboard uses Server-Sent Events (SSE) for zero-latency push; no page refresh is needed. All browser interaction is forwarded to the same input queue used by the terminal, so terminal and browser can be used interchangeably.

---

## Consensus Flow

```
[View starts]
      │
      ▼
Leader: prompts for block data → broadcasts PROPOSE
      │
      ▼
Replicas: validate checksum + locking rule → show Y/N prompt → send VOTE
      │
      ├─ YES votes ≥ quorum  →  Leader forms QC → broadcasts QC
      │                          → 3-chain check → COMMIT if satisfied
      │
      ├─ NO votes > n/2      →  Leader aborts round → NEW_VIEW
      │
      └─ Vote-phase timeout  →  Leader forces VIEW CHANGE → NEW_VIEW
```

---

## Byzantine Behaviour (`-m` flag)

A malicious node:
- Sends a deliberately **corrupted CRC32 checksum** on every proposal
- **Randomly flips** its vote (YES ↔ NO)

Honest replicas **automatically reject** any block whose checksum does not match the data, so a single Byzantine leader cannot get a valid QC from honest nodes.

---

## Example Session (4 terminals, same machine)

```
Terminal 1 (Node 1 — Leader, View 1):
  >>> YOU are the leader for View 1
  Enter block data: hello-world

Terminal 2 (Node 2 — Replica):
  VIEW 1 — PROPOSAL RECEIVED
  Block Data: "hello-world"
  Accept proposal from leader? (y/n): y

Terminal 3 (Node 3 — Replica):
  Accept proposal from leader? (y/n): y

Terminal 4 (Node 4 — Byzantine):
  [BYZANTINE] Sending NO vote (flipped)

Result: 3 YES votes ≥ quorum(3) → QC formed → block accepted
        After 3 such rounds → Block 1 COMMITTED ✓
```

---

## Troubleshooting

| Problem | Cause | Fix |
|---------|-------|-----|
| `Cannot listen on port XXXX` | Node already running | Kill the old process or use a different node ID |
| `No peers found` | Other nodes not started yet | Start all nodes within ~5 seconds of each other |
| Node 2 not getting Y/N prompt | Peer not discovered | Ensure both nodes on same machine started; they auto-discover via localhost |
| `View timeout` / `WARN VIEW TIMER` | Leader silent or crashed | Automatic — view change triggers new leader |
| Replica keeps timing out | Network blocked | Open firewall ports 8001–8004 on both machines |
| QC never forms | Not enough YES votes | Need `2f+1` YES votes; with 4 nodes quorum = 3 |
| Dashboard not loading | Web server port blocked | Open firewall ports 7001–7004; check `Web dashboard →` line in startup output |
| Browser prompt not appearing | SSE connection dropped | Refresh the page; the `/events` endpoint reconnects automatically |

---

## Algorithm Details

**HotStuff** is a linear-complexity BFT protocol:

- **Safety**: The locking rule prevents two conflicting blocks from both receiving a QC. A replica only votes for a proposal that extends its locked block or carries a higher-view QC.
- **Liveness**: View-change (triggered by timeout) guarantees progress even if the current leader is faulty.
- **3-chain commit**:  Block `B` is committed when three consecutive QC'd blocks form a parent-child chain `B ← B' ← B''`. This guarantees irreversibility.
- **Quorum intersection**: Any two quorums of size `2f+1` share at least one honest node, preventing conflicting commits.

---

## License

Educational implementation for distributed systems coursework.
