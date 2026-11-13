// File is a session envelope store that survives restarts.
//
// It wraps Memory for all behaviour (minting rules, scoping, expiry, pruning)
// and adds one thing: after every mutation the whole map is written to a JSON
// file via a temp file and an atomic rename. The envelope is tiny — dates, a
// one-line summary, a turn count — so a full rewrite is cheaper than a log,
// and it cannot leave a half-written file behind.
//
// Conversation content is still not stored here: the business agent owns it.
// Restarting the daemon therefore keeps the ids, dates, summaries and statuses,
// which is all a client needs to resume.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"vireo/internal/protocol"
)

// File persists the envelope store to a JSON file.
type File struct {
	path string
	mem  *Memory
	logf func(format string, args ...any)

	// mu serialises writers so two goroutines never rename over each other.
	mu sync.Mutex
}

// OpenFile loads the store at path, creating it on first use. A missing file is
// not an error: it means "start empty". A corrupt file is an error, because
// silently discarding a client's history would be worse than failing loudly.
func OpenFile(path string, logf func(format string, args ...any)) (*File, error) {
	if path == "" {
		return nil, fmt.Errorf("session: file store needs a path")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	f := &File{path: path, logf: logf, mem: NewMemoryWith(nil)}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var items map[string]protocol.SessionInfo
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("session: %s is not valid JSON: %w", path, err)
		}
		f.mem = NewMemoryWith(items)
	case os.IsNotExist(err):
		// first run
	default:
		return nil, fmt.Errorf("session: read %s: %w", path, err)
	}
	return f, nil
}

// Path returns the backing file.
func (f *File) Path() string { return f.path }

// SetClock overrides the time source, for tests.
func (f *File) SetClock(now func() time.Time) { f.mem.now = now }

// persist writes the current map, atomically.
func (f *File) persist() {
	f.mu.Lock()
	defer f.mu.Unlock()

	snap := f.mem.Snapshot()
	blob, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		f.logf("session: marshal %s: %v", f.path, err)
		return
	}
	if dir := filepath.Dir(f.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			f.logf("session: mkdir %s: %v", dir, err)
			return
		}
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		f.logf("session: write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, f.path); err != nil {
		f.logf("session: rename %s: %v", tmp, err)
	}
}

func (f *File) Create(info protocol.SessionInfo) protocol.SessionInfo {
	out := f.mem.Create(info)
	f.persist()
	return out
}

func (f *File) Get(clientID, agent, id string) (protocol.SessionInfo, bool) {
	return f.mem.Get(clientID, agent, id)
}

func (f *File) Touch(clientID, agent, id, verb, summary string, ttl time.Duration) (protocol.SessionInfo, bool) {
	out, ok := f.mem.Touch(clientID, agent, id, verb, summary, ttl)
	if ok {
		f.persist()
	}
	return out, ok
}

func (f *File) Close(clientID, agent, id string) (protocol.SessionInfo, bool) {
	out, ok := f.mem.Close(clientID, agent, id)
	if ok {
		f.persist()
	}
	return out, ok
}

func (f *File) List(clientID, agent string) []protocol.SessionInfo {
	return f.mem.List(clientID, agent)
}

func (f *File) Prune(now time.Time, retention time.Duration) int {
	removed := f.mem.Prune(now, retention)
	if removed > 0 {
		f.persist()
	}
	return removed
}

func (f *File) Count() Counts { return f.mem.Count() }
