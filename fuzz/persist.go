package fuzz

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Layout inside CorpusDir:
//   corpus/<sha256>     one file per interesting input, named by content hash
//   crashes/<sha256>    minimal crash reproducers
//   crashes/<sha256>.txt panic message for the reproducer
//   quarantine/<name>   corrupt entries moved here instead of failing load
//
// Naming by content hash makes writes idempotent: re-saving an already
// known input is a no-op, so restarts never duplicate entries.

const (
	corpusSubdir     = "corpus"
	crashesSubdir    = "crashes"
	quarantineSubdir = "quarantine"
)

// hashOf returns the content-addressed name for an input.
func hashOf(input []byte) string {
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}

// loadCorpus reads corpus entries from dir. Corrupt or unreadable files
// are moved into the quarantine subdirectory; loading never fails because
// of them. Entries are returned sorted by hash for deterministic replay.
func loadCorpus(dir string) ([][]byte, error) {
	cdir := filepath.Join(dir, corpusSubdir)
	entries, err := os.ReadDir(cdir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read corpus dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out [][]byte
	for _, name := range names {
		path := filepath.Join(cdir, name)
		data, err := os.ReadFile(path)
		if err != nil || hashOf(data) != name {
			// Corrupt or misnamed entry: isolate it, keep loading.
			_ = quarantine(dir, name, data)
			continue
		}
		out = append(out, data)
	}
	return out, nil
}

// quarantine moves a corrupt entry out of the corpus.
func quarantine(dir, name string, data []byte) error {
	qdir := filepath.Join(dir, quarantineSubdir)
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(qdir, name), data, 0o644)
}

// saveCorpusEntry persists one corpus input. Idempotent: an entry whose
// hash already exists on disk is not rewritten.
func saveCorpusEntry(dir string, input []byte) error {
	cdir := filepath.Join(dir, corpusSubdir)
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(cdir, hashOf(input))
	if _, err := os.Stat(path); err == nil {
		return nil // already persisted, do not duplicate
	}
	return writeFileAtomic(path, input)
}

// saveCrash persists a minimal crash reproducer and its panic message.
func saveCrash(dir string, input []byte, info string) error {
	cdir := filepath.Join(dir, crashesSubdir)
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		return err
	}
	name := hashOf(input)
	path := filepath.Join(cdir, name)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := writeFileAtomic(path, input); err != nil {
		return err
	}
	return os.WriteFile(path+".txt", []byte(info), 0o644)
}

// writeFileAtomic writes via a temp file + rename so a crash mid-write
// never leaves a half-written corpus entry.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadCorpusInputs loads previously persisted corpus inputs from dir,
// isolating corrupt entries. Use the result as seeds for a resumed run:
//
//	seeds, _ := fuzz.LoadCorpusInputs(dir)
//	engine.Run(seeds)
func LoadCorpusInputs(dir string) ([][]byte, error) {
	return loadCorpus(dir)
}
