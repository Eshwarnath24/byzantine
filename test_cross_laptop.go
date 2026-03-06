package main

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// Test cross-laptop connectivity
func main() {
	myIP := "10.12.226.231"
	friendIP := "10.12.227.66"
	tcpPorts := []int{7001, 7002, 7003, 7004}
	webPorts := []int{8001, 8002, 8003, 8004}
	bar := strings.Repeat("=", 60)

	fmt.Println(bar)
	fmt.Println("HotStuff Cross-Laptop Connectivity Test")
	fmt.Println(bar)

	fmt.Printf("\n[Local] Your laptop: %s\n", myIP)
	fmt.Printf("[Remote] Friend's laptop: %s\n\n", friendIP)

	// Test TCP consensus ports
	fmt.Println("Testing TCP consensus ports (7001-7004)...")
	for _, port := range tcpPorts {
		nodeID := port - 7000
		testTCPPort(friendIP, port, fmt.Sprintf("Node %d consensus", nodeID))
	}

	// Test web/RPC ports
	fmt.Println("\nTesting web/RPC ports (8001-8004)...")
	for _, port := range webPorts {
		nodeID := port - 8000
		testTCPPort(friendIP, port, fmt.Sprintf("Node %d web/RPC", nodeID))
	}

	fmt.Println("\n" + bar)
	fmt.Println("Results:")
	fmt.Println("  ✓ TCP ports reachable → Direct communication works")
	fmt.Println("  ✗ TCP ports blocked → Firewall may need rules")
	fmt.Println("\nNext steps if blocked:")
	fmt.Println("  1. Friend opens firewall (see FRIEND_COMMANDS.md)")
	fmt.Println("  2. Or use explicit IP args: ./hotstuff.exe 3 1=10.12.226.231")
	fmt.Println(bar)
}

func testTCPPort(host string, port int, label string) {
	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		fmt.Printf("  [✗] %s (:%d): unreachable\n", label, port)
		return
	}
	defer conn.Close()
	fmt.Printf("  [✓] %s (:%d): reachable\n", label, port)
}
