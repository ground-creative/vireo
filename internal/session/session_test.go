package session

import (
	"strings"
	"testing"
	"time"

	"vireo/internal/protocol"
)

func TestMintDefault(t *testing.T) {
	m, err := NewMint("", 0, "", "")
	if err != nil {
		t.Fatalf("NewMint: %v", err)
	}
	if err := m.TestMint(); err != nil {
		t.Fatalf("TestMint: %v", err)
	}
	id, err := m.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(id) != DefaultLength {
		t.Errorf("minted id %q: len = %d, want %d", id, len(id), DefaultLength)
	}
	id2, _ := m.Generate()
	if id == id2 {
		t.Error("two mints produced the same id")
	}
}

func TestMintPrefixAndCharset(t *testing.T) {
	// Legacy shape: bk-123456.
	m, err := NewMint("bk-", 6, CharsetDigits, "")
	if err != nil {
		t.Fatalf("NewMint: %v", err)
	}
	if err := m.TestMint(); err != nil {
		t.Fatalf("TestMint: %v", err)
	}
	id, err := m.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(id, "bk-") || len(id) != 9 {
		t.Errorf("minted id = %q", id)
	}
	for _, c := range id[3:] {
		if c < '0' || c > '9' {
			t.Errorf("minted id %q: non-digit in random part", id)
		}
	}
}

func TestMintDerivedRegexMatches(t *testing.T) {
	m, _ := NewMint("sess-", 8, CharsetHex, "")
	id, err := m.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(id, "sess-") || len(id) != 13 {
		t.Errorf("id = %q", id)
	}
}

func TestMintImpossibleRulesRejected(t *testing.T) {
	// Regex demands digits-only but charset is alphanumeric: the derived
	// candidates can never match the explicit regex.
	m, err := NewMint("", 8, CharsetAlnum, `^[0-9]{8}$`)
	if err != nil {
		t.Fatalf("NewMint: %v", err)
	}
	if err := m.TestMint(); err == nil {
		t.Fatal("impossible mint rules passed the startup test")
	}
}

func TestMintBadRegex(t *testing.T) {
	if _, err := NewMint("", 0, "", "("); err == nil {
		t.Error("invalid regex accepted")
	}
}

func TestStoreScopedGet(t *testing.T) {
	m := NewMemory()
	created := m.Create(protocol.SessionInfo{ID: "s1", Owner: "alice", Agent: "menu-agent"})

	// Right client, right agent.
	if _, ok := m.Get("alice", "menu-agent", "s1"); !ok {
		t.Fatal("owner lookup failed")
	}
	// Wrong client: unknown.
	if _, ok := m.Get("bob", "menu-agent", "s1"); ok {
		t.Error("another client's session was retrievable")
	}
	// Wrong agent: unknown.
	if _, ok := m.Get("alice", "reservations-agent", "s1"); ok {
		t.Error("session retrievable through the wrong agent")
	}
	_ = created
}

func TestStoreTouchAndTurns(t *testing.T) {
	m := NewMemory()
	m.Create(protocol.SessionInfo{ID: "s1", Owner: "alice", Agent: "menu-agent"})

	it, ok := m.Touch("alice", "menu-agent", "s1", "get_menu", "browsed the menu", time.Hour)
	if !ok || it.Turns != 1 {
		t.Fatalf("first touch: %+v ok=%v", it, ok)
	}
	it, ok = m.Touch("alice", "menu-agent", "s1", "place_order", "ordered pizza", time.Hour)
	if !ok || it.Turns != 2 {
		t.Fatalf("second touch: %+v ok=%v", it, ok)
	}
	if it.Summary != "ordered pizza" {
		t.Errorf("summary = %q", it.Summary)
	}
	// Touch with an empty summary keeps the previous one.
	it, _ = m.Touch("alice", "menu-agent", "s1", "place_order", "", time.Hour)
	if it.Summary != "ordered pizza" {
		t.Errorf("summary cleared by empty touch: %q", it.Summary)
	}
	// Wrong scope touch fails.
	if _, ok := m.Touch("bob", "menu-agent", "s1", "x", "", time.Hour); ok {
		t.Error("touch by wrong client succeeded")
	}
}

func TestStoreExpiryAndSlidingTTL(t *testing.T) {
	m := NewMemory()
	base := time.Now()
	m.now = func() time.Time { return base }
	m.Create(protocol.SessionInfo{ID: "s1", Owner: "alice", Agent: "menu-agent"})
	m.Touch("alice", "menu-agent", "s1", "get_menu", "", time.Minute)

	// Halfway: still alive, TTL slides.
	m.now = func() time.Time { return base.Add(30 * time.Second) }
	if _, ok := m.Touch("alice", "menu-agent", "s1", "get_menu", "", time.Minute); !ok {
		t.Fatal("session should be alive after renewal")
	}
	m.now = func() time.Time { return base.Add(80 * time.Second) }
	if _, ok := m.Get("alice", "menu-agent", "s1"); !ok {
		t.Fatal("sliding TTL should keep the session alive")
	}

	// Fully expired.
	m.now = func() time.Time { return base.Add(10 * time.Minute) }
	if _, ok := m.Get("alice", "menu-agent", "s1"); ok {
		t.Error("expired session still retrievable")
	}
	if got := m.List("alice", "menu-agent"); len(got) != 0 {
		t.Errorf("expired session listed: %+v", got)
	}
}

func TestStoreListScopedAndOrdered(t *testing.T) {
	m := NewMemory()
	base := time.Now()
	m.now = func() time.Time { return base }
	m.Create(protocol.SessionInfo{ID: "old", Owner: "alice", Agent: "menu-agent"})
	m.now = func() time.Time { return base.Add(time.Minute) }
	m.Create(protocol.SessionInfo{ID: "new", Owner: "alice", Agent: "menu-agent"})
	m.Create(protocol.SessionInfo{ID: "other-agent", Owner: "alice", Agent: "reservations-agent"})
	m.Create(protocol.SessionInfo{ID: "other-client", Owner: "bob", Agent: "menu-agent"})

	got := m.List("alice", "menu-agent")
	if len(got) != 2 {
		t.Fatalf("list = %d entries, want 2", len(got))
	}
	if got[0].ID != "new" {
		t.Errorf("order = %+v, want newest first", got)
	}
}

func TestStoreCloseScoped(t *testing.T) {
	m := NewMemory()
	m.Create(protocol.SessionInfo{ID: "s1", Owner: "alice", Agent: "menu-agent"})
	if _, ok := m.Close("bob", "menu-agent", "s1"); ok {
		t.Error("close by wrong client succeeded")
	}
	it, ok := m.Close("alice", "menu-agent", "s1")
	if !ok || it.Status != protocol.StatusClosed {
		t.Fatalf("close: %+v ok=%v", it, ok)
	}
	// Closed sessions stay visible as history.
	list := m.List("alice", "menu-agent")
	if len(list) != 1 || list[0].Status != protocol.StatusClosed {
		t.Fatalf("closed session missing from history: %+v", list)
	}
	// A closed session cannot be resumed or touched.
	if _, ok := m.Get("alice", "menu-agent", "s1"); ok {
		t.Error("closed session retrievable")
	}
	if _, ok := m.Touch("alice", "menu-agent", "s1", "x", "", time.Hour); ok {
		t.Error("closed session touchable")
	}
}
