package main

// =============================================================================
// tree.go — Explicit block tree for HotStuff BFT
//
// Each block is stored as a TreeNode with a pointer to its parent and a slice
// of child pointers, making the parent-child relationship first-class rather
// than implicit (previously only a ParentID field in the flat blocks map).
//
// Key operations:
//   • AddBlock   — attach a new block to its parent in the tree
//   • IsAncestor — walk up the tree to test ancestry (locking rule)
//   • MarkQC     — record a QC, advance lockedNode and highQCNode pointers
//   • Commit     — mark committed, prune sibling branches
//   • PrintTree  — ASCII tree diagram to stdout
//
// Thread safety: all methods are called while the caller holds cs.mu.
// =============================================================================

import (
	"fmt"
	"sort"
	"strings"
)

// ─── DATA STRUCTURES ─────────────────────────────────────────────────────────

// TreeNode wraps a Block and carries tree-navigation pointers.
type TreeNode struct {
	Block     *Block
	Parent    *TreeNode
	Children  []*TreeNode
	QCd       bool
	Committed bool
}

// BlockTree is the explicit block tree rooted at a synthetic genesis node
// (Block ID = 0).
type BlockTree struct {
	root       *TreeNode
	nodes      map[int]*TreeNode
	lockedNode *TreeNode // highest ancestor we are locked on (safety)
	highQCNode *TreeNode // node with the highest-view QC seen (liveness)
}

// ─── CONSTRUCTOR ─────────────────────────────────────────────────────────────

// NewBlockTree creates an empty tree with genesis (ID=0, View=0) as the root.
func NewBlockTree() *BlockTree {
	genesis := &TreeNode{
		Block:     &Block{ID: 0, ParentID: -1, View: 0, Data: "genesis"},
		Committed: true,
		QCd:       true,
	}
	bt := &BlockTree{
		root:  genesis,
		nodes: map[int]*TreeNode{0: genesis},
	}
	bt.lockedNode = genesis
	bt.highQCNode = genesis
	return bt
}

// ─── TREE OPERATIONS ─────────────────────────────────────────────────────────

// AddBlock inserts blk into the tree under its parent node.
// Returns an error if the parent is not yet in the tree; the block is ignored
// in that case (the flat blocks map remains the authoritative fallback).
// The operation is idempotent.
func (bt *BlockTree) AddBlock(blk *Block) error {
	if _, exists := bt.nodes[blk.ID]; exists {
		return nil // already present
	}
	parentNode, ok := bt.nodes[blk.ParentID]
	if !ok {
		return fmt.Errorf("parent block %d not in tree", blk.ParentID)
	}
	node := &TreeNode{
		Block:  blk,
		Parent: parentNode,
	}
	parentNode.Children = append(parentNode.Children, node)
	bt.nodes[blk.ID] = node
	return nil
}

// GetNode returns the TreeNode for a block ID, or nil if not present.
func (bt *BlockTree) GetNode(id int) *TreeNode {
	return bt.nodes[id]
}

// IsAncestor returns true if ancestorID is an ancestor of (or equal to)
// descID.  Used to enforce the locking rule: vote only if the proposal
// extends the locked block.
func (bt *BlockTree) IsAncestor(ancestorID, descID int) bool {
	cur := bt.nodes[descID]
	for cur != nil {
		if cur.Block.ID == ancestorID {
			return true
		}
		cur = cur.Parent
	}
	return false
}

// MarkQC records that blockID has a quorum certificate.
// Advances highQCNode (deepest QC'd block) and lockedNode (its parent —
// the 1-chain-back safety anchor).
func (bt *BlockTree) MarkQC(blockID int) {
	node, ok := bt.nodes[blockID]
	if !ok {
		return // block not yet in tree — skip silently
	}
	node.QCd = true
	// Track the deepest QC'd node for liveness (highestQCBlock pointer).
	if node.Block.View > bt.highQCNode.Block.View {
		bt.highQCNode = node
	}
	// Lock on the parent of the newly QC'd node (1-chain back).
	// Only advance the lock — never move it backward.
	if node.Parent != nil && node.Parent.Block.ID != 0 &&
		node.Parent.Block.View > bt.lockedNode.Block.View {
		bt.lockedNode = node.Parent
	}
}

// LockedNodeID returns the block ID of the current locked node.
func (bt *BlockTree) LockedNodeID() int {
	if bt.lockedNode == nil {
		return 0
	}
	return bt.lockedNode.Block.ID
}

// HighQCNodeID returns the block ID of the node with the highest-view QC.
func (bt *BlockTree) HighQCNodeID() int {
	if bt.highQCNode == nil {
		return 0
	}
	return bt.highQCNode.Block.ID
}

// Commit marks blockID and all uncommitted ancestors as committed, prunes
// every sibling branch off the committed path, and returns the list of newly
// committed block IDs (oldest first).
func (bt *BlockTree) Commit(blockID int) []int {
	node, ok := bt.nodes[blockID]
	if !ok || node.Committed {
		return nil
	}
	// Collect the path up to the first already-committed ancestor.
	path := []*TreeNode{}
	for cur := node; cur != nil && !cur.Committed; cur = cur.Parent {
		path = append(path, cur)
	}
	// Commit oldest to newest; prune sibling branches at each step.
	committed := make([]int, 0, len(path))
	for i := len(path) - 1; i >= 0; i-- {
		n := path[i]
		n.Committed = true
		committed = append(committed, n.Block.ID)
		if n.Parent != nil {
			kept := make([]*TreeNode, 0, 1)
			for _, sibling := range n.Parent.Children {
				if sibling == n {
					kept = append(kept, sibling)
				} else {
					bt.pruneSubtree(sibling)
				}
			}
			n.Parent.Children = kept
		}
	}
	return committed
}

// pruneSubtree removes node and all its descendants from bt.nodes.
func (bt *BlockTree) pruneSubtree(node *TreeNode) {
	if node == nil {
		return
	}
	for _, child := range node.Children {
		bt.pruneSubtree(child)
	}
	delete(bt.nodes, node.Block.ID)
}

// ─── ASCII TREE PRINTER ───────────────────────────────────────────────────────

// PrintTree renders the block tree as an ASCII diagram to stdout.
// Must be called while the caller holds cs.mu.
func (bt *BlockTree) PrintTree() {
	fmt.Printf("\n%s%s%s\n", cPurple+cBold, strings.Repeat("─", 60), cReset)
	fmt.Printf("%s  BLOCK TREE%s\n", cPurple+cBold, cReset)
	fmt.Printf("%s%s%s\n", cPurple, strings.Repeat("─", 60), cReset)
	bt.printNode(bt.root, "", true)
	fmt.Printf("  %s(\u2713=committed  *=QC'd  ~=pending  locked=B%d  highQC=B%d)%s\n",
		cDim, bt.LockedNodeID(), bt.HighQCNodeID(), cReset)
	fmt.Printf("%s%s%s\n", cPurple+cBold, strings.Repeat("─", 60), cReset)
}

func (bt *BlockTree) printNode(node *TreeNode, prefix string, isLast bool) {
	if node == nil {
		return
	}
	connector := "├── "
	childPrefix := prefix + "│   "
	if isLast {
		connector = "└── "
		childPrefix = prefix + "    "
	}

	// Status marker
	var status string
	switch {
	case node.Committed:
		status = fmt.Sprintf("%s\u2713%s", cPurple+cBold, cReset)
	case node.QCd:
		status = fmt.Sprintf("%s*%s", cCyan+cBold, cReset)
	default:
		status = fmt.Sprintf("%s~%s", cDim, cReset)
	}

	// Extra annotations
	extra := ""
	if bt.lockedNode != nil && node.Block.ID == bt.lockedNode.Block.ID && node.Block.ID != 0 {
		extra += fmt.Sprintf(" %s[locked]%s", cYellow+cBold, cReset)
	}
	if bt.highQCNode != nil && node.Block.ID == bt.highQCNode.Block.ID && node.Block.ID != 0 {
		extra += fmt.Sprintf(" %s[highQC]%s", cCyan, cReset)
	}

	dataStr := ""
	if node.Block.Data != "" && node.Block.Data != "genesis" {
		dataStr = fmt.Sprintf(" %q", node.Block.Data)
		if len(dataStr) > 22 {
			dataStr = dataStr[:19] + `..."`
		}
	}

	fmt.Printf("  %s%sB%d%s[v%d]%s%s\n",
		prefix, connector,
		node.Block.ID, status, node.Block.View,
		dataStr, extra)

	// Sort children by block ID for deterministic output.
	children := make([]*TreeNode, len(node.Children))
	copy(children, node.Children)
	sort.Slice(children, func(i, j int) bool {
		return children[i].Block.ID < children[j].Block.ID
	})
	for i, child := range children {
		bt.printNode(child, childPrefix, i == len(children)-1)
	}
}
