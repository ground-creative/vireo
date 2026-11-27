package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testCert writes a self-signed certificate for 127.0.0.1 and returns the paths
// to the PEM cert and key.
func testCert(t *testing.T, dir, name string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPath = filepath.Join(dir, name+"-cert.pem")
	keyPath = filepath.Join(dir, name+"-key.pem")
	certOut, _ := os.Create(certPath)
	_ = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	certOut.Close()
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyOut, _ := os.Create(keyPath)
	_ = pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyOut.Close()
	return certPath, keyPath
}

// startTLSServer listens on a loopback port with the given certificate and
// accepts handshakes, so a dial either completes or fails for the TLS reason.
func startTLSServer(t *testing.T, certPath, keyPath string) (addr string, stop func()) {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load key pair: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = c.(*tls.Conn).Handshake()
				_ = c.Close()
			}()
		}
	}()
	return ln.Addr().String(), func() { close(done); ln.Close() }
}

// TestDialCAFileVerifiesSelfSigned is the point of the option: a self-signed
// daemon certificate can be trusted explicitly, with verification still on.
func TestDialCAFileVerifiesSelfSigned(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := testCert(t, dir, "good")
	addr, stop := startTLSServer(t, certPath, keyPath)
	defer stop()

	conn, err := dial(context.Background(), Options{
		Addr:       addr,
		CAFile:     certPath,
		Timeout:    5 * time.Second,
		ServerName: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("dial with CAFile: %v", err)
	}
	_ = conn.Close()
}

// TestDialCAFileStillRejectsAnotherCert is the guard against the option quietly
// becoming -insecure: trusting cert A must not accept cert B.
func TestDialCAFileStillRejectsAnotherCert(t *testing.T) {
	dir := t.TempDir()
	goodCert, _ := testCert(t, dir, "good")
	otherCert, otherKey := testCert(t, dir, "other")
	addr, stop := startTLSServer(t, otherCert, otherKey)
	defer stop()

	_, err := dial(context.Background(), Options{
		Addr:       addr,
		CAFile:     goodCert, // trusts a different certificate
		Timeout:    5 * time.Second,
		ServerName: "127.0.0.1",
	})
	if err == nil {
		t.Fatal("dial succeeded against a certificate not signed by the CA file; the check was bypassed")
	}
	var unknownAuthority bool
	if s := err.Error(); strings.Contains(s, "certificate") {
		unknownAuthority = true
	}
	if !unknownAuthority {
		t.Fatalf("unexpected error shape: %v", err)
	}
}

// TestDialCAFileRejectsBadName covers hostname checking: a trusted certificate
// for another name must still fail.
func TestDialCAFileRejectsBadName(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := testCert(t, dir, "good")
	addr, stop := startTLSServer(t, certPath, keyPath)
	defer stop()

	_, err := dial(context.Background(), Options{
		Addr:       addr,
		CAFile:     certPath,
		Timeout:    5 * time.Second,
		ServerName: "wrong.example", // certificate does not cover this
	})
	if err == nil {
		t.Fatal("dial succeeded for a name the certificate does not cover")
	}
}

// TestRootPoolRejectsNonPEM covers a misconfigured bundle: a file with no
// certificate must fail loudly, not produce an empty pool that trusts nothing
// and reports a confusing handshake error.
func TestRootPoolRejectsNonPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-cert.pem")
	if err := os.WriteFile(path, []byte("this is not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rootPool(path); err == nil {
		t.Fatal("rootPool accepted a file with no certificate")
	}
}

// TestRootPoolMissingFile covers a typo in the path.
func TestRootPoolMissingFile(t *testing.T) {
	if _, err := rootPool(filepath.Join(t.TempDir(), "nope.pem")); err == nil {
		t.Fatal("rootPool accepted a missing file")
	}
}

// TestVerificationHintNamesTheSafeOption pins that the error tells the operator
// about -ca first. The original message offered only -insecure, which pushed
// every local user to disable verification.
func TestVerificationHintNamesTheSafeOption(t *testing.T) {
	err := verificationHint(errTestTLS, Options{Addr: "localhost:7779"})
	if !strings.Contains(err.Error(), "-ca") {
		t.Fatalf("hint does not mention -ca:\n%v", err)
	}
	if !strings.Contains(err.Error(), "verification stays on") {
		t.Fatalf("hint does not make clear that -ca keeps verification:\n%v", err)
	}
}

// TestVerificationHintWithCAFile covers the second attempt: when a bundle was
// already supplied, suggesting -ca again would be useless advice.
func TestVerificationHintWithCAFile(t *testing.T) {
	err := verificationHint(errTestTLS, Options{Addr: "localhost:7779", CAFile: "certs/cert.pem"})
	if strings.Contains(err.Error(), "Pass -ca") {
		t.Fatalf("hint repeats -ca instead of explaining the failure:\n%v", err)
	}
	if !strings.Contains(err.Error(), "certs/cert.pem") {
		t.Fatalf("hint does not name the bundle that was used:\n%v", err)
	}
}

// TestVerificationHintSilentWhenInsecure covers the unsafe path: no advice,
// because the caller already opted out.
func TestVerificationHintSilentWhenInsecure(t *testing.T) {
	err := verificationHint(errTestTLS, Options{Addr: "localhost:7779", Insecure: true})
	if err != errTestTLS {
		t.Fatalf("hint wrapped an -insecure error: %v", err)
	}
}

type testErr struct{}

func (testErr) Error() string { return "tls: failed to verify certificate" }

var errTestTLS = testErr{}
