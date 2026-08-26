package haloyd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haloydev/haloy/internal/config"
)

// writeCert drops a self-signed certificate at <dir>/<domain>.pem, in the
// combined form the proxy reads.
func writeCert(t *testing.T, dir, domain string, sans []string, notAfter time.Time) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		DNSNames:     sans,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	var buf strings.Builder
	if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode certificate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, domain+combinedCertExt), []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
}

func newManager(t *testing.T, certDir string, external []string) *CertificatesManager {
	t.Helper()
	return &CertificatesManager{
		config: CertificatesManagerConfig{
			CertDir:  certDir,
			External: config.CertificatesConfig{External: external},
		},
	}
}

// The whole point: a wildcard certificate can never match canonical+aliases,
// so without the external list ACME would replace it on the first refresh.
func TestCheckExternalCertificateAcceptsAWildcardOriginCert(t *testing.T) {
	dir := t.TempDir()
	writeCert(t, dir, "natadeco.example", []string{"*.natadeco.example", "natadeco.example"}, time.Now().AddDate(15, 0, 0))

	cm := newManager(t, dir, []string{"natadeco.example"})
	logger := slog.New(slog.DiscardHandler)

	if err := cm.checkExternalCertificate(logger, "natadeco.example"); err != nil {
		t.Fatalf("checkExternalCertificate() error = %v, want nil", err)
	}

	// And the ACME path would have disagreed, which is why the skip exists.
	changed, err := cm.hasConfigurationChanged(logger, CertificatesDomain{Canonical: "natadeco.example"})
	if err != nil {
		t.Fatalf("hasConfigurationChanged() error = %v", err)
	}
	if !changed {
		t.Error("hasConfigurationChanged() = false; the test's premise no longer holds")
	}
}

func TestCheckExternalCertificateReportsWhatIsWrong(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		cm := newManager(t, t.TempDir(), []string{"absent.example"})
		err := cm.checkExternalCertificate(slog.New(slog.DiscardHandler), "absent.example")
		if err == nil || !strings.Contains(err.Error(), "no certificate at") {
			t.Fatalf("error = %v, want it to say no certificate is there", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		dir := t.TempDir()
		writeCert(t, dir, "old.example", []string{"old.example"}, time.Now().Add(-24*time.Hour))
		cm := newManager(t, dir, []string{"old.example"})
		err := cm.checkExternalCertificate(slog.New(slog.DiscardHandler), "old.example")
		if err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("error = %v, want it to say the certificate expired", err)
		}
	})

	t.Run("not a certificate", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "junk.example"+combinedCertExt), []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
		cm := newManager(t, dir, []string{"junk.example"})
		err := cm.checkExternalCertificate(slog.New(slog.DiscardHandler), "junk.example")
		if err == nil || !strings.Contains(err.Error(), "cannot parse") {
			t.Fatalf("error = %v, want a parse complaint", err)
		}
	})
}

// One edge-issued "*.example.test" serving every subdomain is the ordinary
// shape, and the proxy already falls back to it — so the check has to too.
func TestCheckExternalCertificateFallsBackToTheWildcard(t *testing.T) {
	dir := t.TempDir()
	writeCert(t, dir, "*.example.test", []string{"*.example.test", "example.test"}, time.Now().AddDate(15, 0, 0))

	cm := newManager(t, dir, []string{"one.example.test", "two.example.test"})
	logger := slog.New(slog.DiscardHandler)

	for _, domain := range []string{"one.example.test", "two.example.test"} {
		if err := cm.checkExternalCertificate(logger, domain); err != nil {
			t.Errorf("checkExternalCertificate(%q) error = %v, want nil — the wildcard covers it", domain, err)
		}
	}
}

// An apex has no parent to wildcard, so the exact file is the only answer.
func TestCheckExternalCertificateHasNoWildcardForAnApex(t *testing.T) {
	dir := t.TempDir()
	writeCert(t, dir, "*.example.test", []string{"*.example.test"}, time.Now().AddDate(15, 0, 0))

	cm := newManager(t, dir, []string{"example.test"})
	err := cm.checkExternalCertificate(slog.New(slog.DiscardHandler), "example.test")
	if err == nil || !strings.Contains(err.Error(), "no certificate at") {
		t.Fatalf("error = %v, want it to ask for example.test.pem", err)
	}
}
