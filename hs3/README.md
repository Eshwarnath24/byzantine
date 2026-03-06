# HotStuff BFT Consensus - Complete Implementation

## Overview
This is a complete, fully working implementation of the HotStuff Byzantine Fault Tolerant (BFT) consensus protocol with terminal-based interaction for data entry and vote approval/rejection.

## Features
- **Terminal-Based Interaction**: Enter data and approve/reject proposals directly from the terminal
- **Byzantine Fault Tolerance**: Tolerates f = (n-1)/3 Byzantine failures
- **3-Chain Commit Rule**: Blocks are finalized after 3 consecutive QCs
- **Leader Rotation**: Leaders rotate based on view number
- **Automatic Peer Discovery**: Nodes automatically find each other on the network
- **Timeout Mechanisms**: View change and vote phase timeouts prevent stalling
- **Crash Detection**: Automatic removal of unresponsive nodes
- **Visual Display**: Color-coded logging and blockchain state visualization

## How to Run

### Prerequisites
- Go 1.20 or later installed
- At least 4 separate terminal windows

### Starting the Network

Open 4 separate terminal windows and navigate to the hs3 directory in each. Then run:

**Terminal 1:**
```
go run hotstuff.go 1
```

**Terminal 2:**
```
go run hotstuff.go 2
```

**Terminal 3:**
```
go run hotstuff.go 3
```

**Terminal 4:**
```
go run hotstuff.go 4
```

### Using the System

#### As a Leader
When it's your turn to be the leader (indicated by ">>> YOU are the leader"), you'll be prompted to enter block data:
```
Enter block data: <type your data here>
```

#### As a Replica
When you receive a proposal from the leader, you'll see:
```
Accept proposal from leader? (y/n): 
```

- Type **y** or **yes** to APPROVE the proposal
- Type **n** or **no** to REJECT the proposal

#### Leaving the Cluster
Type **exit** at any prompt to gracefully leave the cluster.

## Consensus Parameters

- **Total Nodes**: 4 (configurable by running more instances)
- **Quorum Size**: 2f + 1 (3 out of 4 nodes for 4-node cluster)
- **Fault Tolerance**: f = (n-1)/3 (1 Byzantine fault for 4-node cluster)
- **View Timeout**: 15 seconds
- **Vote Phase Timeout**: 10 seconds
- **Base Port**: 8000 (Node 1 uses 8001, Node 2 uses 8002, etc.)

## Consensus Flow

1. **Propose Phase**: Leader proposes a block to all replicas
2. **Vote Phase**: Replicas vote YES or NO on the proposal
3. **QC Formation**: If quorum (3/4) vote YES, a Quorum Certificate is formed
4. **Commit Phase**: After 3 consecutive QCs form a valid 3-chain, the oldest block commits

## Blockchain State

The system displays:
- **QC-Accepted Blocks**: Blocks that have received quorum but are pending finality
- **Committed Blocks**: Finalized blocks that cannot be reverted (marked with )
- **Blockchain Height**: Number of committed blocks
- **Current View**: The current consensus round number

## Example Session

```
Terminal 1:
  YOU are the leader for View 1
  Enter block data: transaction-batch-1
  
Terminal 2:
  VIEW 1 — PROPOSAL RECEIVED
  Block Data: "transaction-batch-1"
  Accept proposal from leader? (y/n): y
  
Terminal 3:
  Accept proposal from leader? (y/n): y
  
Terminal 4:
  Accept proposal from leader? (y/n): n

Result: Quorum reached (3/4 YES votes), QC formed, block accepted
```

## Rejection Behavior

- If **strict majority** (>50%) vote NO, the proposal is immediately rejected
- The view advances without adding the block
- A new leader is selected for the next view

## Troubleshooting

**"Cannot listen on port XXXX"**
- Another instance is already running on that node  ID
- Kill the existing process or use a different node ID

**"No peers found"**
- Make sure other nodes are running before starting this one
- Check that ports 8001-8009 are not blocked by firewall

**"View timeout"**
- Leader didn't propose in time
- Automatic view change will occur

## Algorithm Details

**HotStuff** is a leader-based BFT consensus protocol that achieves:
- **Linear view change**: O(n) communication complexity
- **Responsiveness**: Progress depends only on network delay
- **Simplicity**: Clean 3-phase commit (prepare, pre-commit, commit)

The 3-chain commit rule ensures safety: a block  is finalized only after 3 consecutive QCs form a parent-child chain, guaranteeing that conflicting blocks cannot both commit.

## Files

- hotstuff.go - Complete implementation (single file)
- go.mod - Go module definition
- README.md - This documentation

## License

Educational implementation for distributed systems coursework.
