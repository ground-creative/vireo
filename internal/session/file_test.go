package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"vireo/internal/protocol"
)

func newFileStore(t *testing.T) (*File, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.json")
	f, err := OpenFile(path, nil)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	return f, path
}

func envelope(id, owner, agent string) protocol.SessionInfo {
	return protocol.SessionInfo{ID: id, Owner: owner, Agent: agent, Summary: "first"}
}

func TestFileSurvivesReopen(t *testing.T) {
	f, path := newFileStore(t)
	ttl := time.Hour

	f.Create(envelope("s1", "alice", "menu-agent"))
	if _, ok := f.Touch("alice", "menu-agent", "s1", "place_order", "ordered a pizza", ttl); !ok {
		t.Fatal("Touch should succeed")
	}
	if _, ok := f.Close("alice", "menu-agent", "s1"); !ok {
		t.Fatal("Close should succeed")
	}

	// Reopen as a new process would.
	again, err := OpenFile(path, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	c := again.Count()
	if c.Total != 1 || c.Closed != 1 {
		t.Fatalf("counts after reopen = %+v, want 1 closed", c)
	}

	list := again.List("alice", "menu-agent")
	if len(list) != 1 {
		t.Fatalf("list after reopen = %d entries", len(list))
	}
	got := list[0]
	if got.ID != "s1" || got.Status != protocol.StatusClosed || got.Summary != "ordered a pizza" || got.Turns != 1 {
		t.Fatalf("restored envelope = %+v", got)
	}

	// Scoping still holds after a reopen.
	if _, ok := again.Get("bob", "menu-agent", "s1"); ok {
		t.Error("another client must not see alice's session")
	}
}

func TestFileScopeKeptOnDisk(t *testing.T) {
	f, path := newFileStore(t)
	f.Create(envelope("s1", "alice", "menu-agent"))

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, want := range []string{`"s1"`, `"alice"`, `"menu-agent"`} {
		if !contains(string(raw), want) {
			t.Errorf("file is missing %s: %s", want, raw)
		}
	}
	// Nothing secret, no conversation content.
	if contains(string(raw), "question") {
		t.Errorf("store should hold envelopes only: %s", raw)
	}
}

func TestFileRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFile(path, nil); err == nil {
		t.Fatal("a corrupt store must fail loudly, not start empty")
	}
}

func TestFileMissingIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "sessions.json")
	f, err := OpenFile(path, nil)
	if err != nil {
		t.Fatalf("missing file should be an empty store: %v", err)
	}
	if c := f.Count(); c.Total != 0 {
		t.Fatalf("counts = %+v", c)
	}
	// A create must also create the directory.
	f.Create(envelope("s1", "alice", "menu-agent"))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store not written: %v", err)
	}
}

func TestPruneRemovesFinishedOnly(t *testing.T) {
	base := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	now := base
	f, _ := newFileStore(t)
	f.SetClock(func() time.Time { return now })

	ttl := 30 * time.Minute
	f.Create(envelope("open", "alice", "menu-agent"))
	f.Create(envelope("closed-old", "alice", "menu-agent"))
	f.Create(envelope("closed-new", "alice", "menu-agent"))
	f.Create(envelope("expired-old", "alice", "menu-agent"))

	// Close two at base.
	f.Close("alice", "menu-agent", "closed-old")
	f.Close("alice", "menu-agent", "closed-new")
	// Touch one so it expires: ttl slides from base.
	f.Touch("alice", "menu-agent", "expired-old", "get_menu", "", ttl)

	// Move to base+2h: closed-old is 2h old, closed-new gets re-closed later.
	now = base.Add(2 * time.Hour)
	f.Close("alice", "menu-agent", "closed-new")

	retention := time.Hour
	removed := f.Prune(now, retention)
	if removed != 2 {
		t.Fatalf("pruned %d, want 2 (closed-old and expired-old)", removed)
	}
	c := f.Count()
	if c.Total != 2 || c.Open != 1 || c.Closed != 1 || c.Expired != 0 {
		t.Fatalf("counts after prune = %+v", c)
	}
	if _, ok := f.Get("alice", "menu-agent", "open"); !ok {
		t.Error("open sessions must never be pruned")
	}
}

func TestPruneZeroRetentionIsNoop(t *testing.T) {
	f, _ := newFileStore(t)
	f.Create(envelope("s1", "alice", "menu-agent"))
	f.Close("alice", "menu-agent", "s1")
	if n := f.Prune(time.Now().Add(10*365*24*time.Hour), 0); n != 0 {
		t.Fatalf("prune with zero retention removed %d", n)
	}
}

func TestPrunePersists(t *testing.T) {
	f, path := newFileStore(t)
	base := time.Now()
	f.SetClock(func() time.Time { return base })
	f.Create(envelope("s1", "alice", "menu-agent"))
	f.Close("alice", "menu-agent", "s1")

	if n := f.Prune(base.Add(48*time.Hour), time.Hour); n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}

	again, err := OpenFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := again.Count(); c.Total != 0 {
		t.Fatalf("prune was not persisted: %+v", c)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
