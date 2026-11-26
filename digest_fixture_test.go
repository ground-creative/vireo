package main

// digest_fixture_test.go checks this daemon's manifest digest against the
// specification's own fixture (machine/fixtures/digest.json).
//
// MAN-40 defines the digest over RFC 8785 canonical JSON. A probe written in
// another language must compute the same string for the same document, so the
// fixture carries expected digests rather than leaving each implementation to
// trust its own canonicaliser. This test is the Go half of that pair; the
// specification's probe is the other.
//
// Skipped when the specification repository is not checked out beside this one,
// so a standalone checkout still builds.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"vireo/internal/protocol"
)

const digestFixture = "../agent-front-door-spec/machine/fixtures/digest.json"

type digestCase struct {
	Name     string         `json:"name"`
	Document map[string]any `json:"document"`
	Digest   string         `json:"digest"`
}

func TestDigestMatchesSpecificationFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(digestFixture))
	if err != nil {
		t.Skipf("specification fixture not available: %v", err)
	}
	var doc struct {
		Cases []digestCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("fixture carries no cases")
	}

	for _, tc := range doc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Digest == "PENDING" {
				t.Skip("fixture digest not yet published")
			}
			// The digest is defined over the document as published (MAN-40), so
			// the bytes hashed are the fixture's own JSON — exactly what a
			// client receives on the wire. Decoding into a struct first and
			// re-serialising would insert members the document never carried
			// ("session": {}) and produce a digest no other implementation can
			// reproduce. That is the defect this test exists to catch.
			raw, err := json.Marshal(tc.Document)
			if err != nil {
				t.Fatalf("marshal document: %v", err)
			}
			got, err := protocol.DigestOfDocument(raw)
			if err != nil {
				t.Fatalf("compute digest: %v", err)
			}
			if got != tc.Digest {
				t.Errorf("digest = %s, want %s (MAN-40: canonical form disagrees with the specification)",
					got, tc.Digest)
			}
		})
	}
}
