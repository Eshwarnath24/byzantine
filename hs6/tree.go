package main

import (
	"fmt"
	"sort"
	"strings"
)

type TreeNode struct {
	Block     *Block
	Parent    *TreeNode
	Children  []*TreeNode
	QCd       bool
	Committed bool
}

type BlockTree struct {
	root       *TreeNode
	nodes      map[int]*TreeNode
	lockedNode *TreeNode
	highQCNode *TreeNode
}

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

func (bt *BlockTree) AddBlock(blk *Block) error {
	if _, exists := bt.nodes[blk.ID]; exists {
		return nil
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

func (bt *BlockTree) GetNode(id int) *TreeNode {
	return bt.nodes[id]
}

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

func (bt *BlockTree) MarkQC(blockID int) {
	node, ok := bt.nodes[blockID]
	if !ok {
		return
	}
	node.QCd = true

	if node.Block.View > bt.highQCNode.Block.View {
		bt.highQCNode = node
	}

	if node.Parent != nil && node.Parent.Block.ID != 0 &&
		node.Parent.Block.View > bt.lockedNode.Block.View {
		bt.lockedNode = node.Parent
	}
}

func (bt *BlockTree) LockedNodeID() int {
	if bt.lockedNode == nil {
		return 0
	}
	return bt.lockedNode.Block.ID
}

func (bt *BlockTree) HighQCNodeID() int {
	if bt.highQCNode == nil {
		return 0
	}
	return bt.highQCNode.Block.ID
}

func (bt *BlockTree) Commit(blockID int) []int {
	node, ok := bt.nodes[blockID]
	if !ok || node.Committed {
		return nil
	}

	path := []*TreeNode{}
	for cur := node; cur != nil && !cur.Committed; cur = cur.Parent {
		path = append(path, cur)
	}

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

func (bt *BlockTree) pruneSubtree(node *TreeNode) {
	if node == nil {
		return
	}
	for _, child := range node.Children {
		bt.pruneSubtree(child)
	}
	delete(bt.nodes, node.Block.ID)
}

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

	var status string
	switch {
	case node.Committed:
		status = fmt.Sprintf("%s\u2713%s", cPurple+cBold, cReset)
	case node.QCd:
		status = fmt.Sprintf("%s*%s", cCyan+cBold, cReset)
	default:
		status = fmt.Sprintf("%s~%s", cDim, cReset)
	}

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

	children := make([]*TreeNode, len(node.Children))
	copy(children, node.Children)
	sort.Slice(children, func(i, j int) bool {
		return children[i].Block.ID < children[j].Block.ID
	})
	for i, child := range children {
		bt.printNode(child, childPrefix, i == len(children)-1)
	}
}
