package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

type NodeConfig struct {
	ID int    `json:"id"`
	IP string `json:"ip"`
}

type ClusterConfig struct {
	Nodes []NodeConfig `json:"nodes"`
}

func main() {
	nodeID := flag.Int("id", 0, "Your node ID from config.json (example: 1 or 3)")
	mode := flag.String("mode", "server", "Run mode: server or client")
	port := flag.Int("port", 9090, "TCP port for this sample chat")
	configPath := flag.String("config", "config.json", "Path to config.json")
	flag.Parse()

	if *nodeID <= 0 {
		fmt.Println("Usage: go run ./test/test.go -id <node_id> -mode <server|client>")
		os.Exit(1)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Printf("Failed to read config: %v\n", err)
		os.Exit(1)
	}

	myIP, friendIP, err := resolveTwoLaptopIPs(cfg, *nodeID)
	if err != nil {
		fmt.Printf("Config error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Node ID: %d\n", *nodeID)
	fmt.Printf("My laptop IP: %s\n", myIP)
	fmt.Printf("Friend laptop IP: %s\n", friendIP)
	fmt.Printf("Mode: %s\n", strings.ToLower(*mode))
	fmt.Printf("Port: %d\n", *port)

	switch strings.ToLower(*mode) {
	case "server":
		err = runServer(*port)
	case "client":
		err = runClient(friendIP, *port)
	default:
		err = fmt.Errorf("invalid mode %q (use server or client)", *mode)
	}

	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

func loadConfig(path string) (ClusterConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ClusterConfig{}, err
	}

	var cfg ClusterConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ClusterConfig{}, err
	}

	if len(cfg.Nodes) == 0 {
		return ClusterConfig{}, errors.New("config has no nodes")
	}

	return cfg, nil
}

// resolveTwoLaptopIPs returns local and remote laptop IPs for a two-laptop setup.
func resolveTwoLaptopIPs(cfg ClusterConfig, myNodeID int) (string, string, error) {
	myIP := ""
	uniqueIPs := make(map[string]bool)

	for _, n := range cfg.Nodes {
		ip := strings.TrimSpace(n.IP)
		if ip == "" {
			continue
		}
		uniqueIPs[ip] = true
		if n.ID == myNodeID {
			myIP = ip
		}
	}

	if myIP == "" {
		return "", "", fmt.Errorf("node id %d not found in config", myNodeID)
	}

	if len(uniqueIPs) < 2 {
		return "", "", errors.New("need at least 2 distinct IPs in config for two laptops")
	}

	ipList := make([]string, 0, len(uniqueIPs))
	for ip := range uniqueIPs {
		ipList = append(ipList, ip)
	}
	sort.Strings(ipList)

	friendIP := ""
	for _, ip := range ipList {
		if ip != myIP {
			friendIP = ip
			break
		}
	}

	if friendIP == "" {
		return "", "", errors.New("could not find friend laptop IP")
	}

	return myIP, friendIP, nil
}

func runServer(port int) error {
	listenAddr := fmt.Sprintf("0.0.0.0:%d", port)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	defer ln.Close()

	fmt.Printf("Server listening on %s\n", listenAddr)
	fmt.Println("Waiting for client connection...")

	conn, err := ln.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()

	fmt.Printf("Connected: %s\n", conn.RemoteAddr().String())
	return chatLoop(conn)
}

func runClient(friendIP string, port int) error {
	addr := fmt.Sprintf("%s:%d", friendIP, port)
	fmt.Printf("Connecting to %s ...\n", addr)

	var conn net.Conn
	var err error

	for i := 0; i < 20; i++ {
		conn, err = net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("could not connect to %s: %w", addr, err)
	}
	defer conn.Close()

	fmt.Printf("Connected: %s\n", conn.RemoteAddr().String())
	return chatLoop(conn)
}

func chatLoop(conn net.Conn) error {
	done := make(chan struct{})

	go func() {
		defer close(done)
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if !errors.Is(err, io.EOF) {
					fmt.Printf("Read error: %v\n", err)
				}
				return
			}
			fmt.Printf("\nFriend: %s", line)
			fmt.Print("You: ")
		}
	}()

	fmt.Println("Type messages and press Enter. Type /exit to quit.")
	stdin := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("You: ")
		if !stdin.Scan() {
			break
		}

		msg := strings.TrimSpace(stdin.Text())
		if msg == "" {
			continue
		}
		if msg == "/exit" {
			return nil
		}

		payload := fmt.Sprintf("%s\n", msg)
		if _, err := conn.Write([]byte(payload)); err != nil {
			return err
		}
	}

	if err := stdin.Err(); err != nil {
		return err
	}

	<-done
	return nil
}
