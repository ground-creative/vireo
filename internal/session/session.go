// Package session holds the mint rules and the envelope store for v2
// server-minted, client-scoped sessions.
//
// The daemon mints every session id according to per-agent rules (prefix,
// length, charset, regex). Sessions are born bound to one agent and are
// always looked up scoped by (client id, agent, session id). The store
// keeps only the envelope — dates, a one-line summary, turn count — never
// conversation content, which stays with the business agent.
package session

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"vireo/internal/protocol"
)

// Charsets for the random part of a minted id.
const (
	CharsetAlnum   = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	CharsetDigits  = "0123456789"
	CharsetHex     = "0123456789abcdef"
	CharsetLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

const (
	// DefaultLength is the default number of random chars per id.
	DefaultLength = 32
	// mintRetries bounds candidate generation against the validation regex.
	mintRetries = 8
)

// Mint generates session ids for one agent's resolved rules.
type Mint struct {
	prefix  string
	length  int
	charset string
	re      *regexp.Regexp
	rule    string
}

// NewMint builds a minter. When regex is empty it is derived from prefix +
// charset + length, so minting always satisfies validation by construction.
func NewMint(prefix string, length int, charset, regex string) (*Mint, error) {
	if prefix == "" && length == 0 && charset == "" && regex == "" {
		prefix, length, charset = "", DefaultLength, CharsetAlnum
	}
	if length <= 0 {
		length = DefaultLength
	}
	if charset == "" {
		charset = CharsetAlnum
	}
	m := &Mint{prefix: prefix, length: length, charset: charset}
	if regex == "" {
		regex = fmt.Sprintf(`^%s[%s]{%d}$`, regexp.QuoteMeta(prefix), regexpClass(charset), length)
	}
	re, err := regexp.Compile(regex)
	if err != nil {
		return nil, fmt.Errorf("session: invalid id regex %q: %w", regex, err)
	}
	m.re = re
	m.rule = regex
	return m, nil
}

// Generate returns one id matching the rules.
func (m *Mint) Generate() (string, error) {
	for i := 0; i < mintRetries; i++ {
		id := m.prefix + randomString(m.length, m.charset)
		if m.re.MatchString(id) {
			return id, nil
		}
	}
	return "", fmt.Errorf("session: mint rules %q cannot produce a valid id (prefix/charset/length conflict with the regex)", m.rule)
}

// TestMint checks at startup that the resolved rules can actually mint.
func (m *Mint) TestMint() error {
	if _, err := m.Generate(); err != nil {
		return err
	}
	return nil
}

// Rule returns the validation regex, for logs.
func (m *Mint) Rule() string { return m.rule }

func randomString(n int, charset string) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(charset)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand failure: fall back to a time-derived digit.
			b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
			continue
		}
		b[i] = charset[idx.Int64()]
	}
	return string(b)
}

// regexpClass maps a literal charset to a character-class fragment for the
// derived validation regex.
func regexpClass(charset string) string {
	switch charset {
	case CharsetDigits:
		return "0-9"
	case CharsetHex:
		return "0-9a-f"
	case CharsetLetters:
		return "A-Za-z"
	default:
		return "A-Za-z0-9_-"
	}
}

// Store is the envelope store interface the daemon keeps.
type Store interface {
	Create(info protocol.SessionInfo) protocol.SessionInfo
	Get(clientID, agent, id string) (protocol.SessionInfo, bool)
	Touch(clientID, agent, id, verb, summary string, ttl time.Duration) (protocol.SessionInfo, bool)
	Close(clientID, agent, id string) (protocol.SessionInfo, bool)
	List(clientID, agent string) []protocol.SessionInfo

	// Prune deletes records that are finished and older than retention:
	// expired sessions past ExpiresAt+retention and closed sessions past
	// UpdatedAt+retention. Open sessions are never pruned. A retention of
	// zero deletes nothing (age is required to decide).
	Prune(now time.Time, retention time.Duration) int
	// Count reports how many records the store holds, by status.
	Count() Counts
}

// Counts is the store census, for logs and the prune command.
type Counts struct {
	Open    int `json:"open"`
	Closed  int `json:"closed"`
	Expired int `json:"expired"`
	Total   int `json:"total"`
}

// Memory is the in-process envelope store. Sessions are lost on restart
// unless they are wrapped by a persistent store such as File.
type Memory struct {
	mu    sync.Mutex
	items map[string]protocol.SessionInfo
	now   func() time.Time
}

func NewMemory() *Memory {
	return &Memory{items: map[string]protocol.SessionInfo{}, now: time.Now}
}

// NewMemoryWith seeds a store from records loaded off disk.
func NewMemoryWith(items map[string]protocol.SessionInfo) *Memory {
	if items == nil {
		items = map[string]protocol.SessionInfo{}
	}
	return &Memory{items: items, now: time.Now}
}

// Snapshot copies every record, for persistence and diagnostics.
func (m *Memory) Snapshot() map[string]protocol.SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]protocol.SessionInfo, len(m.items))
	for id, it := range m.items {
		out[id] = it
	}
	return out
}

// Prune deletes finished records older than retention.
func (m *Memory) Prune(now time.Time, retention time.Duration) int {
	if retention <= 0 {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for id, it := range m.items {
		it = m.expireLocked(it)
		switch it.Status {
		case protocol.StatusExpired:
			if it.ExpiresAt.IsZero() || it.ExpiresAt.Add(retention).After(now) {
				m.items[id] = it
				continue
			}
		case protocol.StatusClosed:
			if it.UpdatedAt.Add(retention).After(now) {
				m.items[id] = it
				continue
			}
		default:
			m.items[id] = it
			continue
		}
		delete(m.items, id)
		removed++
	}
	return removed
}

// Count reports the census, without mutating the store.
func (m *Memory) Count() Counts {
	m.mu.Lock()
	defer m.mu.Unlock()
	var c Counts
	for _, it := range m.items {
		switch m.expireLocked(it).Status {
		case protocol.StatusOpen:
			c.Open++
		case protocol.StatusClosed:
			c.Closed++
		case protocol.StatusExpired:
			c.Expired++
		}
		c.Total++
	}
	return c
}

func (m *Memory) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *Memory) expireLocked(it protocol.SessionInfo) protocol.SessionInfo {
	if it.Status == protocol.StatusOpen && !it.ExpiresAt.IsZero() && m.clock().After(it.ExpiresAt) {
		it.Status = protocol.StatusExpired
	}
	return it
}

// Create records a freshly minted session envelope.
func (m *Memory) Create(info protocol.SessionInfo) protocol.SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	info.CreatedAt = now
	info.UpdatedAt = now
	info.Turns = 0
	info.Status = protocol.StatusOpen
	m.items[info.ID] = info
	return info
}

// Get returns the envelope scoped by client and agent. A session belonging to
// anyone else, or to another agent, behaves as unknown.
func (m *Memory) Get(clientID, agent, id string) (protocol.SessionInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[id]
	if !ok {
		return protocol.SessionInfo{}, false
	}
	it = m.expireLocked(it)
	m.items[id] = it
	// Only open sessions are retrievable: closed behaves like expired, so a
	// finished session cannot be resumed, listed, or touched.
	if it.Status != protocol.StatusOpen || it.Owner != clientID || it.Agent != agent {
		return protocol.SessionInfo{}, false
	}
	return it, true
}

// Touch updates a session after a message: slides the TTL, bumps the turn
// count and keeps the best summary available.
func (m *Memory) Touch(clientID, agent, id, verb, summary string, ttl time.Duration) (protocol.SessionInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[id]
	if !ok || it.Owner != clientID || it.Agent != agent || it.Status != protocol.StatusOpen {
		return protocol.SessionInfo{}, false
	}
	now := m.clock()
	it.UpdatedAt = now
	if ttl > 0 {
		it.ExpiresAt = now.Add(ttl)
	}
	if summary != "" {
		it.Summary = summary
	}
	it.Turns++
	m.items[id] = it
	return it, true
}

// Close marks a session closed, scoped by client and agent.
func (m *Memory) Close(clientID, agent, id string) (protocol.SessionInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[id]
	if !ok || it.Owner != clientID || it.Agent != agent {
		return protocol.SessionInfo{}, false
	}
	it = m.expireLocked(it)
	it.Status = protocol.StatusClosed
	it.UpdatedAt = m.clock()
	m.items[id] = it
	return it, true
}

// List returns a client's sessions with one agent, newest activity first.
// Closed sessions stay visible as history; only expired records are hidden.
func (m *Memory) List(clientID, agent string) []protocol.SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]protocol.SessionInfo, 0, len(m.items))
	for id, it := range m.items {
		it = m.expireLocked(it)
		m.items[id] = it
		if it.Status == protocol.StatusExpired || it.Owner != clientID || it.Agent != agent {
			continue
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

// String renders a mint's rules for logs.
func MintRule(prefix string, length int, charset string) string {
	return strings.TrimSpace(prefix + fmt.Sprintf("[%s]{%d}", regexpClass(charset), length))
}
