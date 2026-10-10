package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

func TestParseProcessedMachineIdentifier(t *testing.T) {
	value, err := parseProcessedMachineIdentifier([]byte(`<?xml version="1.0"?><Preferences ProcessedMachineIdentifier="test-identifier" PlexOnlineToken="must-not-be-used"/>`))
	if err != nil {
		t.Fatal(err)
	}
	if value != "test-identifier" {
		t.Fatalf("identifier = %q", value)
	}
}

func TestDerivePassword(t *testing.T) {
	const expected = "85945214b2de4a14962ce3b603d3a1bd7aead046f11ba6a36d7b83c51b0a1d02ca8993f10b50864d10a149319cdf2487afec2dc5feef064cc887a2fd5c193fad"
	if got := derivePassword("example"); got != expected {
		t.Fatal("derived password mismatch")
	}
}

func TestValidateCertificate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "*.example.test"},
		DNSNames:     []string{"*.example.test"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, root, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(root)
	if err := validateDecodedCertificate(leafKey, leaf, []*x509.Certificate{root}, "server.example.test", now, roots); err != nil {
		t.Fatalf("valid certificate rejected: %v", err)
	}
	if err := validateDecodedCertificate(leafKey, leaf, []*x509.Certificate{root}, "wrong.invalid", now, roots); err == nil {
		t.Fatal("certificate with wrong hostname was accepted")
	}
}

func TestCertificateLoaderKeepsLastGoodAndAdoptsValidReplacement(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(10),
		Subject:               pkix.Name{CommonName: "Reload Test Root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}

	makeBundle := func(serial int64) []byte {
		t.Helper()
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leafTemplate := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: "*.example.test"},
			DNSNames:     []string{"*.example.test"},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, root, &leafKey.PublicKey, rootKey)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(leafDER)
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := pkcs12.Encode(rand.Reader, leafKey, leaf, []*x509.Certificate{root}, derivePassword("reload-test"))
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}

	directory := t.TempDir()
	p12Path := filepath.Join(directory, "cert.p12")
	preferencesPath := filepath.Join(directory, "Preferences.xml")
	if err := os.WriteFile(preferencesPath, []byte(`<Preferences ProcessedMachineIdentifier="reload-test"/>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p12Path, makeBundle(11), 0o600); err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(root)
	store := &certificateStore{}
	loader := certificateLoader{
		config: config{
			p12Path:         p12Path,
			preferencesPath: preferencesPath,
			hostname:        "server.example.test",
		},
		store: store,
		roots: roots,
	}
	if err := loader.refresh(); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	initial := store.active.Load()
	if initial == nil || initial.Leaf.SerialNumber.Int64() != 11 {
		t.Fatal("initial certificate was not activated")
	}

	if err := os.WriteFile(p12Path, []byte("invalid replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loader.refresh(); err == nil {
		t.Fatal("invalid replacement was accepted")
	}
	if store.active.Load() != initial {
		t.Fatal("invalid replacement displaced the active certificate")
	}

	if err := os.WriteFile(p12Path, makeBundle(12), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loader.refresh(); err != nil {
		t.Fatalf("valid replacement refresh: %v", err)
	}
	replacement := store.active.Load()
	if replacement == nil || replacement == initial || replacement.Leaf.SerialNumber.Int64() != 12 {
		t.Fatal("valid replacement was not activated")
	}
}
