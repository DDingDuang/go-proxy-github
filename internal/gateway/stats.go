package gateway

import (
	"sort"
	"sync"
	"time"
)

// Stats 轻量内存统计(仅用于管理面板展示, 进程重启即清零)
type Stats struct {
	mu         sync.Mutex
	startedAt  time.Time
	requests   int64
	connects   int64
	activeConn int64
	bytesUp    int64 // 客户端 → 上游
	bytesDown  int64 // 上游 → 客户端
	errors     int64
	byHost     map[string]*hostStat
}

type hostStat struct {
	requests   int64
	connects   int64
	errors     int64
	lastStatus int
	lastAt     time.Time
}

// Snapshot 统计快照(JSON 序列化给管理面板)
type Snapshot struct {
	Uptime     int64      `json:"uptime"` // 秒
	Requests   int64      `json:"requests"`
	Connects   int64      `json:"connects"`
	ActiveConn int64      `json:"active_connects"`
	BytesUp    int64      `json:"bytes_up"`
	BytesDown  int64      `json:"bytes_down"`
	Errors     int64      `json:"errors"`
	Hosts      []HostStat `json:"hosts"`
}

// HostStat 单个域名的访问统计
type HostStat struct {
	Host       string    `json:"host"`
	Requests   int64     `json:"requests"`
	Connects   int64     `json:"connects"`
	Errors     int64     `json:"errors"`
	LastStatus int       `json:"last_status"`
	LastAt     time.Time `json:"last_at"`
}

// NewStats 创建统计器
func NewStats() *Stats {
	return &Stats{startedAt: time.Now(), byHost: make(map[string]*hostStat)}
}

// RecordRequest 记录一次普通 HTTP 反向代理请求
func (s *Stats) RecordRequest(host string, status, bytes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	if status >= 500 {
		s.errors++
	}
	h := s.hostStat(host)
	h.requests++
	if status >= 500 {
		h.errors++
	}
	h.lastStatus = status
	h.lastAt = time.Now()
	s.bytesDown += int64(bytes)
}

// BeginConnect 记录一条 CONNECT 隧道建立
func (s *Stats) BeginConnect(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeConn++
}

// EndConnect 记录一条 CONNECT 隧道关闭
func (s *Stats) EndConnect(host string, up, down int64, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeConn--
	s.connects++
	s.bytesUp += up
	s.bytesDown += down
	if failed {
		s.errors++
	}
	h := s.hostStat(host)
	h.connects++
	if failed {
		h.errors++
	}
	h.lastAt = time.Now()
}

// hostStat 返回(或创建)host 的统计项, 调用方需持有锁
func (s *Stats) hostStat(host string) *hostStat {
	h, ok := s.byHost[host]
	if !ok {
		h = &hostStat{}
		s.byHost[host] = h
	}
	return h
}

// Snapshot 返回当前统计快照(按访问量排序, 最多 50 条)
func (s *Stats) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		Uptime:     int64(time.Since(s.startedAt).Seconds()),
		Requests:   s.requests,
		Connects:   s.connects,
		ActiveConn: s.activeConn,
		BytesUp:    s.bytesUp,
		BytesDown:  s.bytesDown,
		Errors:     s.errors,
		Hosts:      make([]HostStat, 0, len(s.byHost)),
	}
	for host, h := range s.byHost {
		snap.Hosts = append(snap.Hosts, HostStat{
			Host:       host,
			Requests:   h.requests,
			Connects:   h.connects,
			Errors:     h.errors,
			LastStatus: h.lastStatus,
			LastAt:     h.lastAt,
		})
	}
	sort.Slice(snap.Hosts, func(i, j int) bool {
		a, b := snap.Hosts[i], snap.Hosts[j]
		ta, tb := a.Requests+a.Connects, b.Requests+b.Connects
		if ta != tb {
			return ta > tb
		}
		return a.Host < b.Host
	})
	if len(snap.Hosts) > 50 {
		snap.Hosts = snap.Hosts[:50]
	}
	return snap
}
