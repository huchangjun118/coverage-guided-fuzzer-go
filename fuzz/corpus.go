package fuzz

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Entry 是语料库中的一条输入。
type Entry struct {
	Data []byte
	Hash string // 内容哈希，作为磁盘文件名与去重键
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// corpusStore 管理内存语料与磁盘持久化。
// 只有命中新覆盖的输入才会被 Add；重复内容按哈希去重，
// 保证语料库不会无限膨胀。
type corpusStore struct {
	mu      sync.Mutex
	entries []Entry
	byHash  map[string]struct{}
	dir     string // 空表示仅内存
}

func newCorpusStore(dir string) *corpusStore {
	return &corpusStore{byHash: make(map[string]struct{}), dir: dir}
}

// Add 去重后加入语料；返回是否为新条目。
// 持久化采用“写临时文件 + 原子 rename”，已存在的文件不重复写入。
func (s *corpusStore) Add(data []byte) (Entry, bool) {
	h := hashOf(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byHash[h]; dup {
		return Entry{}, false
	}
	if s.dir != "" {
		if err := writeAtomic(filepath.Join(s.dir, h), data); err != nil {
			// 持久化失败不阻断运行，仅内存保存。
			fmt.Fprintf(os.Stderr, "fuzz: persist corpus entry: %v\n", err)
		}
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	e := Entry{Data: cp, Hash: h}
	s.entries = append(s.entries, e)
	s.byHash[h] = struct{}{}
	return e, true
}

// Snapshot 返回当前语料的有序快照（按加入顺序，确定性）。
func (s *corpusStore) Snapshot() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// Len 返回语料条数。
func (s *corpusStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// load 从磁盘恢复语料。损坏（哈希与内容不符、不可读）的条目
// 被移入 quarantine 子目录隔离，不会导致启动失败。
func (s *corpusStore) load() error {
	if s.dir == "" {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	// 按文件名排序加载，保证恢复后的语料顺序确定。
	var names []string
	err := filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != s.dir && d.Name() == "quarantine" {
				return filepath.SkipDir
			}
			return nil
		}
		names = append(names, p)
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range names {
		data, err := os.ReadFile(p)
		if err != nil || hashOf(data) != filepath.Base(p) {
			s.quarantine(p)
			continue
		}
		h := hashOf(data)
		if _, dup := s.byHash[h]; dup {
			continue
		}
		s.entries = append(s.entries, Entry{Data: data, Hash: h})
		s.byHash[h] = struct{}{}
	}
	return nil
}

func (s *corpusStore) quarantine(p string) {
	qdir := filepath.Join(s.dir, "quarantine")
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		return
	}
	dst := filepath.Join(qdir, filepath.Base(p))
	if _, err := os.Stat(dst); errors.Is(err, fs.ErrNotExist) {
		_ = os.Rename(p, dst)
	} else {
		_ = os.Remove(p)
	}
}

// writeAtomic 先写临时文件再 rename，崩溃中断不会留下半写文件；
// 目标已存在时直接跳过，避免重复写入。
func writeAtomic(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
