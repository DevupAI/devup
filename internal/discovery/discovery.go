package discovery

import (
	"encoding/json"
	"net"
	"os"
	"sync"
	"time"

	"devup/internal/logging"
	"devup/internal/version"
)

const (
	MulticastGroup = "239.0.0.1:9999"
	HeartbeatEvery = 5 * time.Second
	PeerTimeout    = 15 * time.Second
	MaxPacketSize  = 1024
)

// Heartbeat is the UDP payload broadcast by each agent.
type Heartbeat struct {
	NodeID     string  `json:"node_id"`
	Addr       string  `json:"addr"`
	Port       int     `json:"port"`
	SlotsFree  int     `json:"slots_free"`
	ActiveJobs int     `json:"active_jobs"`
	Version    string  `json:"version"`
	MemTotalMB int     `json:"mem_total_mb,omitempty"`
	MemFreeMB  int     `json:"mem_free_mb,omitempty"`
	LoadAvg1   float64 `json:"load_avg_1,omitempty"`
}

// Peer represents a discovered node in the cluster.
type Peer struct {
	NodeID     string
	Addr       string
	Port       int
	SlotsFree  int
	ActiveJobs int
	Version    string
	MemTotalMB int
	MemFreeMB  int
	LoadAvg1   float64
	LastSeen   time.Time
}

// NodeStats is the snapshot returned by the callback on every heartbeat tick.
type NodeStats struct {
	SlotsFree  int
	ActiveJobs int
	MemTotalMB int
	MemFreeMB  int
	LoadAvg1   float64
}

// NodeStatsFunc is called before each heartbeat to collect dynamic node telemetry.
type NodeStatsFunc func() NodeStats

// Service manages UDP multicast discovery: broadcasting heartbeats and
// listening for peers. Zero external dependencies — raw net.UDPConn only.
type Service struct {
	nodeID    string
	port      int
	statsFn   NodeStatsFunc
	localAddr string

	mu    sync.RWMutex
	peers map[string]*Peer // keyed by NodeID

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New creates a discovery service. The agent HTTP port is passed so peers know
// how to reach each other. statsFn is called on every heartbeat tick to collect
// slot count, memory, and load telemetry.
func New(nodeID string, agentPort int, statsFn NodeStatsFunc) *Service {
	return &Service{
		nodeID:  nodeID,
		port:    agentPort,
		statsFn: statsFn,
		peers:   make(map[string]*Peer),
		stopCh:  make(chan struct{}),
	}
}

// Start launches the broadcaster and listener goroutines.
func (s *Service) Start() error {
	addr, err := net.ResolveUDPAddr("udp4", MulticastGroup)
	if err != nil {
		return err
	}

	s.localAddr = detectLocalIP()

	// Listener: join multicast group
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		return err
	}
	conn.SetReadBuffer(MaxPacketSize * 16)

	// Broadcaster: separate dial conn
	sendConn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		conn.Close()
		return err
	}

	s.wg.Add(3)
	go s.broadcast(sendConn)
	go s.listen(conn)
	go s.reaper()

	logging.Info("discovery started", "node_id", s.nodeID, "multicast", MulticastGroup, "local_addr", s.localAddr)
	return nil
}

// Stop shuts down the discovery service gracefully.
func (s *Service) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

// Peers returns a snapshot of all known peers (including self).
func (s *Service) Peers() []Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Peer, 0, len(s.peers))
	for _, p := range s.peers {
		out = append(out, *p)
	}
	return out
}

// NodeID returns the local node identifier.
func (s *Service) NodeID() string {
	return s.nodeID
}

func (s *Service) broadcast(conn *net.UDPConn) {
	defer s.wg.Done()
	defer conn.Close()

	ticker := time.NewTicker(HeartbeatEvery)
	defer ticker.Stop()

	send := func() {
		stats := s.statsFn()
		hb := Heartbeat{
			NodeID:     s.nodeID,
			Addr:       s.localAddr,
			Port:       s.port,
			SlotsFree:  stats.SlotsFree,
			ActiveJobs: stats.ActiveJobs,
			Version:    version.Version,
			MemTotalMB: stats.MemTotalMB,
			MemFreeMB:  stats.MemFreeMB,
			LoadAvg1:   stats.LoadAvg1,
		}
		data, err := json.Marshal(hb)
		if err != nil {
			logging.Error("discovery: marshal heartbeat", "err", err)
			return
		}
		if _, err := conn.Write(data); err != nil {
			logging.Error("discovery: send heartbeat", "err", err)
		}
	}

	// Send immediately on start, then on tick
	send()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			send()
		}
	}
}

func (s *Service) listen(conn *net.UDPConn) {
	defer s.wg.Done()
	defer conn.Close()

	buf := make([]byte, MaxPacketSize)
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			select {
			case <-s.stopCh:
				return
			default:
				logging.Error("discovery: read", "err", err)
				continue
			}
		}

		var hb Heartbeat
		if err := json.Unmarshal(buf[:n], &hb); err != nil {
			continue
		}
		if hb.NodeID == "" {
			continue
		}

		s.mu.Lock()
		s.peers[hb.NodeID] = &Peer{
			NodeID:     hb.NodeID,
			Addr:       hb.Addr,
			Port:       hb.Port,
			SlotsFree:  hb.SlotsFree,
			ActiveJobs: hb.ActiveJobs,
			Version:    hb.Version,
			MemTotalMB: hb.MemTotalMB,
			MemFreeMB:  hb.MemFreeMB,
			LoadAvg1:   hb.LoadAvg1,
			LastSeen:   time.Now(),
		}
		s.mu.Unlock()
	}
}

// reaper marks peers that haven't sent a heartbeat within PeerTimeout as gone
// by removing them from the map entirely.
func (s *Service) reaper() {
	defer s.wg.Done()
	ticker := time.NewTicker(PeerTimeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			for id, p := range s.peers {
				if now.Sub(p.LastSeen) > PeerTimeout {
					logging.Info("discovery: peer expired", "node_id", id)
					delete(s.peers, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

// detectLocalIP returns the best non-loopback IPv4 address. Falls back to
// hostname if no suitable interface is found.
func detectLocalIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		if h, err := os.Hostname(); err == nil {
			return h
		}
		return "127.0.0.1"
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}
			return ip.String()
		}
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "127.0.0.1"
}
