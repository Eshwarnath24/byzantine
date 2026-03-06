package main

import (
	"fmt"
	"net"
	"time"
)

// Quick test to verify connectivity between two laptops
// Usage: go run test_connection.go
func testConnection() {
	myIP := "10.12.226.231"
	friendIP := "10.12.227.66"
	tcpPorts := []int{7001, 7002, 7003, 7004}
	webPorts := []int{8001, 8002, 8003, 8004}

	fmt.Println("=" * 60)
	fmt.Println("HotStuff Cross-Laptop Connectivity Test")
	fmt.Println("=" * 60)

	fmt.Printf("\n[LOCAL] Your laptop: %s\n", myIP)
	fmt.Printf("[REMOTE] Friend's laptop: %s\n\n", friendIP)

	// Test TCP ports on friend's laptop
	fmt.Println("Testing TCP consensus ports on friend's laptop...")
	for _, port := range tcpPorts {
		nodeID := port - 7000
		addr := fmt.Sprintf("%s:%d", friendIP, port)
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			fmt.Printf("  [✗] Node %d (port %d): unreachable\n", nodeID, port)
		} else {
			defer conn.Close()
			fmt.Printf("  [✓] Node %d (port %d): reachable\n", nodeID, port)
		}
	}

	// Test web/RPC ports
	fmt.Println("\nTesting web/RPC ports on friend's laptop...")
	for _, port := range webPorts {
		nodeID := port - 8000
		addr := fmt.Sprintf("%s:%d", friendIP, port)
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			fmt.Printf("  [✗] Node %d web (port %d): unreachable\n", nodeID, port)
		} else {
			defer conn.Close()
			fmt.Printf("  [✓] Node %d web (port %d): reachable\n", nodeID, port)
		}
	}

	// Test UDP multicast
	fmt.Println("\nTesting UDP multicast discovery...")
	testUDPMulticast()

	fmt.Println("\n" + string([]byte("="*60)))
	fmt.Println("Interpretation:")
	fmt.Println("  [✓] All TCP ports → Nodes can directly communicate")
	fmt.Println("  [✓] UDP test passes → Auto-discovery works")
	fmt.Println("  [✗] Some ports blocked → Use Option 2 (explicit IPs) in FRIEND_COMMANDS.md")
	fmt.Println("=" * 60)
}

func testUDPMulticast() {
	multicastGroup := "224.0.0.100:8888"
	addr, err := net.ResolveUDPAddr("udp", multicastGroup)
	if err != nil {
		fmt.Printf("  [✗] UDP multicast impossible: %v\n", err)
		return
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		fmt.Printf("  [✗] UDP multicast send failed: %v\n", err)
		return
	}
	defer conn.Close()

	msg := []byte("test beacon")
	if _, err := conn.Write(msg); err != nil {
		fmt.Printf("  [✗] UDP write failed: %v\n", err)
		return
	}

	fmt.Printf("  [✓] UDP multicast can broadcast\n")
}

func main() {
	testConnection()
}
