package gateway

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// CheckStore Git 项目检查记录的 SQLite 持久化存储
type CheckStore struct {
	db *sql.DB
}

// OpenCheckStore 打开(不存在则创建)SQLite 数据库并建表
func OpenCheckStore(path string) (*CheckStore, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据库目录失败: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// 单写者场景(网关本身)无需 WAL, 但开启可提升并发读体验
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS repo_checks (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			time       TEXT    NOT NULL,
			ip         TEXT    NOT NULL,
			project    TEXT    NOT NULL,
			method     TEXT    NOT NULL,
			status     INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_repo_checks_id ON repo_checks(id DESC);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化数据表失败: %w", err)
	}
	return &CheckStore{db: db}, nil
}

// Insert 写入一条检查记录
func (s *CheckStore) Insert(e LogEntry) error {
	_, err := s.db.Exec(
		`INSERT INTO repo_checks (time, ip, project, method, status, latency_ms) VALUES (?, ?, ?, ?, ?, ?)`,
		e.Time.UTC().Format(time.RFC3339Nano), e.IP, e.Project, e.Method, e.Status, e.LatencyMS,
	)
	return err
}

// List 分页查询(按 id 倒序, 最新在前)
func (s *CheckStore) List(page, pageSize int) ([]LogEntry, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM repo_checks`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(
		`SELECT time, ip, project, method, status, latency_ms
		 FROM repo_checks ORDER BY id DESC LIMIT ? OFFSET ?`,
		pageSize, (page-1)*pageSize,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	entries := make([]LogEntry, 0, pageSize)
	for rows.Next() {
		var e LogEntry
		var ts string
		if err := rows.Scan(&ts, &e.IP, &e.Project, &e.Method, &e.Status, &e.LatencyMS); err != nil {
			return nil, 0, err
		}
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			e.Time = t.Local()
		}
		e.Kind = "repo_check"
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

// Close 关闭数据库
func (s *CheckStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
