package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"vireo/internal/frame"
	"vireo/internal/protocol"
)

// The specification carries language-neutral fixtures. Running against them is
// what turns "vireo implements the spec" from a claim into a check.
//
// The checkout is located through AGENT_FRONT_DOOR_SPEC so that `go test ./...`
// works in a bare checkout of this repository. scripts/check-conformance.sh
// sets it for you.

func specDir(t *testing.T) string {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("AGENT_FRONT_DOOR_SPEC"))
	if dir == "" {
		t.Skip("conformance fixtures not run: set AGENT_FRONT_DOOR_SPEC to the agent-front-door-spec checkout")
	}
	if _, err := os.Stat(filepath.Join(dir, "machine", "fixtures")); err != nil {
		t.Skipf("conformance fixtures not run: %v", err)
	}
	return dir
}

func loadFixture(t *testing.T, dir, name string, into any) {
	t.Helper()
	path := filepath.Join(dir, "machine", "fixtures", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
}

// frameCase mirrors machine/fixtures/frames.json.
type frameCase struct {
	Name        string  `json:"name"`
	Requirement string  `json:"requirement"`
	StreamID    uint32  `json:"stream_id"`
	TypeName    string  `json:"type"`
	TypeCode    uint8   `json:"type_code"`
	Payload     *string `json:"payload"`
	Hex         string  `json:"hex"`
	Expect      string  `json:"expect"`
	Error       string  `json:"error"`
}

// PROTO-3, PROTO-4, PROTO-5, PROTO-6: every frame fixture either decodes and
// re-encodes to the same bytes, or is refused for the documented reason.
func TestConformanceFrames(t *testing.T) {
	dir := specDir(t)

	var doc struct {
		Cases []frameCase `json:"cases"`
	}
	loadFixture(t, dir, "frames.json", &doc)
	if len(doc.Cases) == 0 {
		t.Fatal("frames.json has no cases")
	}

	for _, tc := range doc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			raw, err := hex.DecodeString(tc.Hex)
			if err != nil {
				t.Fatalf("fixture hex is not hex: %v", err)
			}

			f, readErr := frame.NewReader(bytes.NewReader(raw)).Read()

			switch tc.Expect {
			case "reject":
				if readErr == nil {
					t.Fatalf("%s: frame was accepted, want rejection (%s)", tc.Requirement, tc.Error)
				}
				return
			case "accept":
			default:
				t.Fatalf("unknown expect %q", tc.Expect)
			}

			// Header-only fixtures describe a length that is legal but whose
			// payload is deliberately not included; decoding one would block, so
			// check the header arithmetic instead.
			if tc.Payload == nil {
				if len(raw) < frame.HeaderSize {
					t.Fatalf("header-only fixture is only %d bytes", len(raw))
				}
				length := int(raw[5])<<24 | int(raw[6])<<16 | int(raw[7])<<8 | int(raw[8])
				if length > frame.MaxPayload {
					t.Fatalf("fixture length %d exceeds MaxPayload %d", length, frame.MaxPayload)
				}
				return
			}

			if readErr != nil {
				t.Fatalf("%s: frame was refused: %v", tc.Requirement, readErr)
			}
			if f.StreamID != tc.StreamID {
				t.Errorf("stream_id = %d, want %d", f.StreamID, tc.StreamID)
			}
			if f.Type != tc.TypeCode {
				t.Errorf("type = %d, want %d (%s)", f.Type, tc.TypeCode, tc.TypeName)
			}
			if string(f.Payload) != *tc.Payload {
				t.Errorf("payload = %q, want %q", string(f.Payload), *tc.Payload)
			}

			var out bytes.Buffer
			if err := frame.Write(&out, f); err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			if got := hex.EncodeToString(out.Bytes()); got != tc.Hex {
				t.Errorf("re-encoded %s, want %s", got, tc.Hex)
			}
		})
	}
}

// manifestCase mirrors the two lists in machine/fixtures/manifests.json.
type manifestCase struct {
	Name        string          `json:"name"`
	Requirement string          `json:"requirement"`
	Schema      string          `json:"schema"`
	Document    json.RawMessage `json:"document"`
}

// MAN-47, MAN-48: a document that violates a structural rule is refused rather
// than half-accepted.
func TestConformanceManifestsRejected(t *testing.T) {
	dir := specDir(t)

	var doc struct {
		Cases []manifestCase `json:"cases"`
	}
	loadFixture(t, dir, "manifests.json", &doc)

	for _, tc := range doc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var m protocol.Manifest
			if err := json.Unmarshal(tc.Document, &m); err != nil {
				// A document whose types are wrong is rejected too, and that is
				// the outcome the fixture asks for.
				return
			}
			if err := m.ValidateAsClient(); err == nil {
				t.Fatalf("%s: manifest was accepted, want rejection (%s)", tc.Requirement, tc.Name)
			}
		})
	}
}

// MAN-3, MAN-7, MAN-44, MAN-53: a document a client MUST accept is accepted,
// including the legacy 1.0 shape and unknown members.
func TestConformanceManifestsAccepted(t *testing.T) {
	dir := specDir(t)

	var doc struct {
		Accepted []manifestCase `json:"accepted"`
	}
	loadFixture(t, dir, "manifests.json", &doc)

	for _, tc := range doc.Accepted {
		t.Run(tc.Name, func(t *testing.T) {
			var m protocol.Manifest
			if err := json.Unmarshal(tc.Document, &m); err != nil {
				t.Fatalf("%s: accepted document did not decode: %v", tc.Requirement, err)
			}
			if err := m.ValidateAsClient(); err != nil {
				t.Fatalf("%s: accepted document was refused: %v", tc.Requirement, err)
			}
			if m.Normalize().ManifestVersion == "" {
				t.Error("Normalize left manifest_version empty")
			}
		})
	}
}

// The fixtures also pin the *encoded* form of a manifest, so the Go structs and
// the specification cannot drift silently. This asserts that the structs carry
// every member the full example uses; an added spec field without a matching Go
// field shows up here as an unknown key.
func TestConformanceFullExampleFieldsAreRepresented(t *testing.T) {
	dir := specDir(t)

	raw, err := os.ReadFile(filepath.Join(dir, "machine", "examples", "manifest.full.json"))
	if err != nil {
		t.Fatalf("read full example: %v", err)
	}
	var m protocol.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("full example does not decode into protocol.Manifest: %v", err)
	}
	reencoded, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}

	var want, got map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode example: %v", err)
	}
	if err := json.Unmarshal(reencoded, &got); err != nil {
		t.Fatalf("decode re-encoded: %v", err)
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("manifest member %q is not represented in protocol.Manifest", key)
		}
	}
}

// A cheap guard against fixture drift: every requirement ID a fixture cites
// must exist in the normative text.
func TestConformanceFixtureRequirementsExist(t *testing.T) {
	dir := specDir(t)

	normative := map[string]bool{}
	for _, name := range []string{"MANIFEST.md", "PROTOCOL.md", "CONFORMANCE.md", "SECURITY.md"} {
		raw, err := os.ReadFile(filepath.Join(dir, "normative", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "| ") {
				continue
			}
			cells := strings.Split(line, "|")
			if len(cells) < 3 {
				continue
			}
			id := strings.TrimSpace(cells[1])
			if isRequirementID(id) {
				normative[id] = true
			}
		}
	}
	if len(normative) == 0 {
		t.Fatal("no requirement IDs found in the normative index tables")
	}

	cited := map[string]string{}
	for _, name := range []string{"frames.json", "handshake.json", "manifests.json", "webhook.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, "machine", "fixtures", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, id := range requirementIDs(string(raw)) {
			cited[id] = name
		}
	}
	for id, where := range cited {
		if !normative[id] {
			t.Errorf("%s cites %s, which is not in any normative index", where, id)
		}
	}
}

func isRequirementID(s string) bool {
	for _, prefix := range []string{"MAN-", "PROTO-", "WH-", "SEC-", "CONF-"} {
		if strings.HasPrefix(s, prefix) {
			_, err := strconv.Atoi(strings.TrimPrefix(s, prefix))
			return err == nil
		}
	}
	return false
}

// requirementIDs pulls requirement IDs out of a fixture file. It is deliberately
// loose: a false positive produces a clear failure, and a false negative only
// makes the check weaker.
func requirementIDs(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		for _, prefix := range []string{"MAN-", "PROTO-", "WH-", "SEC-", "CONF-"} {
			if !strings.HasPrefix(s[i:], prefix) {
				continue
			}
			j := i + len(prefix)
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j > i+len(prefix) {
				out = append(out, s[i:j])
			}
			i = j - 1
			break
		}
	}
	return out
}

var _ = errors.Is
