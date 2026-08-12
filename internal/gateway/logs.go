package gateway

import (
	"sync"
	"time"
)

// LogEntry 一条访问日志(管理面板实时展示)
type LogEntry struct {
	Time      time.Time `json:"time"`
	IP        string    `json:"ip"`
	Project   string    `json:"project"` // 目标项目地址, 如 github.com/owner/repo
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
	LatencyMS int64     `json:"latency_ms"`
	Bytes     int64     `json:"bytes"`
	Kind      string    `json:"kind"` // repo_check | proxy | manage
}

// LogBuffer 内存环形日志缓冲(仅用于管理面板, 进程重启即清空)
type LogBuffer struct {
	mu      sync.Mutex
	entries []LogEntry
	max     int
}

// NewLogBuffer 创建最多保留 max 条的日志缓冲
func NewLogBuffer(max int) *LogBuffer {
	if max <= 0 {
		max = 200
	}
	return &LogBuffer{max: max}
}

// Add 追加一条日志
func (b *LogBuffer) Add(e LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = append(b.entries, e)
	if len(b.entries) > b.max {
		b.entries = b.entries[len(b.entries)-b.max:]
	}
}

// List 返回最新的 limit 条日志(时间倒序, 最新在前)
func (b *LogBuffer) List(limit int) []LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(b.entries)
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]LogEntry, n)
	for i := 0; i < n; i++ {
		out[i] = b.entries[len(b.entries)-1-i]
	}
	return out
}
