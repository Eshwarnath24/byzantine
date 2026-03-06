package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type NodeConfig struct {
	ID int    `json:"id"`
	IP string `json:"ip"`
}

type ClusterConfig struct {
	Nodes []NodeConfig `json:"nodes"`
}

type ChatMessage struct {
	From      string `json:"from"`
	Text      string `json:"text"`
	At        string `json:"at"`
	Direction string `json:"direction"`
}

type ChatServer struct {
	myNodeID int
	myIP     string
	friendIP string
	port     int

	mu         sync.Mutex
	history    []ChatMessage
	clients    map[chan ChatMessage]bool
	httpClient *http.Client
}

func main() {
	nodeID := flag.Int("id", 0, "Your node ID from config.json (example: 1 or 3)")
	port := flag.Int("port", 9090, "Web chat HTTP port")
	configPath := flag.String("config", "config.json", "Path to config.json")
	flag.Parse()

	if *nodeID <= 0 {
		fmt.Println("Usage: go run ./test/test.go -id <node_id> -port 9090")
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

	cs := &ChatServer{
		myNodeID:   *nodeID,
		myIP:       myIP,
		friendIP:   friendIP,
		port:       *port,
		history:    make([]ChatMessage, 0, 128),
		clients:    make(map[chan ChatMessage]bool),
		httpClient: &http.Client{Timeout: 3 * time.Second},
	}

	if err := cs.run(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

func (cs *ChatServer) run() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", cs.handleIndex)
	mux.HandleFunc("/events", cs.handleEvents)
	mux.HandleFunc("/api/send", cs.handleSend)
	mux.HandleFunc("/api/receive", cs.handleReceive)
	mux.HandleFunc("/api/history", cs.handleHistory)

	addr := fmt.Sprintf("0.0.0.0:%d", cs.port)
	fmt.Printf("Node ID: %d\n", cs.myNodeID)
	fmt.Printf("My laptop IP: %s\n", cs.myIP)
	fmt.Printf("Friend laptop IP: %s\n", cs.friendIP)
	fmt.Printf("Web chat URL: http://localhost:%d\n", cs.port)
	fmt.Printf("Friend must also run: go run ./test/test.go -id <friend_id> -port %d\n", cs.port)
	return http.ListenAndServe(addr, mux)
}

func (cs *ChatServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

func (cs *ChatServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	cs.mu.Lock()
	copyHistory := make([]ChatMessage, len(cs.history))
	copy(copyHistory, cs.history)
	cs.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(copyHistory)
}

func (cs *ChatServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan ChatMessage, 64)
	cs.mu.Lock()
	cs.clients[ch] = true
	cs.mu.Unlock()
	defer func() {
		cs.mu.Lock()
		delete(cs.clients, ch)
		cs.mu.Unlock()
		close(ch)
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			data, _ := json.Marshal(msg)
			fmt.Fprintf(w, "event: message\n")
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (cs *ChatServer) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}

	msg := ChatMessage{
		From:      fmt.Sprintf("Node %d", cs.myNodeID),
		Text:      text,
		At:        time.Now().Format("15:04:05"),
		Direction: "out",
	}
	cs.storeAndBroadcast(msg)

	forwardOK := cs.forwardToFriend(text)
	res := map[string]any{"ok": true, "forwarded": forwardOK}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (cs *ChatServer) handleReceive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		From string `json:"from"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	from := strings.TrimSpace(req.From)
	if from == "" {
		from = "Friend"
	}

	msg := ChatMessage{
		From:      from,
		Text:      text,
		At:        time.Now().Format("15:04:05"),
		Direction: "in",
	}
	cs.storeAndBroadcast(msg)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (cs *ChatServer) forwardToFriend(text string) bool {
	body, _ := json.Marshal(map[string]string{
		"from": fmt.Sprintf("Node %d", cs.myNodeID),
		"text": text,
	})
	url := fmt.Sprintf("http://%s:%d/api/receive", cs.friendIP, cs.port)
	resp, err := cs.httpClient.Post(url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (cs *ChatServer) storeAndBroadcast(msg ChatMessage) {
	cs.mu.Lock()
	cs.history = append(cs.history, msg)
	for ch := range cs.clients {
		select {
		case ch <- msg:
		default:
		}
	}
	cs.mu.Unlock()
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

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Two Laptop Web Chat</title>
  <style>
    body { font-family: Segoe UI, sans-serif; max-width: 820px; margin: 24px auto; padding: 0 12px; }
    h1 { margin: 0 0 8px; }
    #status { color: #555; margin-bottom: 12px; }
    #log { border: 1px solid #ddd; border-radius: 8px; padding: 12px; min-height: 320px; max-height: 420px; overflow-y: auto; background: #fafafa; }
    .msg { margin: 8px 0; }
    .out { color: #0b5; }
    .in { color: #06c; }
    .meta { color: #777; font-size: 12px; margin-right: 8px; }
    .row { display: flex; gap: 8px; margin-top: 12px; }
    #text { flex: 1; padding: 10px; border-radius: 8px; border: 1px solid #bbb; }
    button { padding: 10px 14px; border-radius: 8px; border: 1px solid #333; background: #111; color: #fff; cursor: pointer; }
  </style>
</head>
<body>
  <h1>Two Laptop Web Chat</h1>
  <div id="status">Connecting...</div>
  <div id="log"></div>
  <div class="row">
    <input id="text" placeholder="Type message and press Send" />
    <button id="sendBtn">Send</button>
  </div>

  <script>
    const log = document.getElementById('log');
    const statusEl = document.getElementById('status');
    const textEl = document.getElementById('text');
    const sendBtn = document.getElementById('sendBtn');

    function addMsg(m) {
      const div = document.createElement('div');
      div.className = 'msg ' + (m.direction === 'out' ? 'out' : 'in');
      div.innerHTML = '<span class="meta">[' + m.at + ']</span><b>' + escapeHtml(m.from) + ':</b> ' + escapeHtml(m.text);
      log.appendChild(div);
      log.scrollTop = log.scrollHeight;
    }

    function escapeHtml(s) {
      return s.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
    }

    async function loadHistory() {
      const res = await fetch('/api/history');
      const arr = await res.json();
      for (const m of arr) addMsg(m);
    }

    async function sendMessage() {
      const text = textEl.value.trim();
      if (!text) return;
      textEl.value = '';
      const res = await fetch('/api/send', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text })
      });
      const out = await res.json();
      if (!out.forwarded) {
        statusEl.textContent = 'Sent locally, but friend laptop is unreachable right now.';
      }
    }

    sendBtn.addEventListener('click', sendMessage);
    textEl.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') sendMessage();
    });

    const ev = new EventSource('/events');
    ev.addEventListener('message', (evt) => {
      addMsg(JSON.parse(evt.data));
      statusEl.textContent = 'Connected';
    });
    ev.onerror = () => {
      statusEl.textContent = 'Reconnecting stream...';
    };

    loadHistory();
  </script>
</body>
</html>`
