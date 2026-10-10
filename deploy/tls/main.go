package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

const (
	defaultListenAddr   = ":32499"
	defaultBackendURL   = "http://wonkvod-backend:32499"
	defaultPollInterval = time.Minute
)

type config struct {
	listenAddr      string
	backendURL      string
	p12Path         string
	preferencesPath string
	hostname        string
	pollInterval    time.Duration
}

type preferences struct {
	ProcessedMachineIdentifier string `xml:"ProcessedMachineIdentifier,attr"`
}

type certificateStore struct {
	active atomic.Pointer[tls.Certificate]
}

func (s *certificateStore) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	certificate := s.active.Load()
	if certificate == nil {
		return nil, errors.New("TLS certificate is not loaded")
	}
	return certificate, nil
}

type certificateLoader struct {
	config      config
	store       *certificateStore
	roots       *x509.CertPool
	lastDigest  [sha256.Size]byte
	hasLastGood bool
}

func main() {
	logger := log.New(os.Stderr, "wonkvod-tls: ", log.LstdFlags)

	cfg, err := loadConfig()
	if err != nil {
		logger.Fatal(err)
	}

	backend, err := url.Parse(cfg.backendURL)
	if err != nil {
		logger.Fatalf("invalid backend URL: %v", err)
	}

	store := &certificateStore{}
	loader := &certificateLoader{config: cfg, store: store}
	for {
		if err := loader.refresh(); err == nil {
			break
		} else {
			logger.Printf("certificate not ready: %v; retrying", err)
			time.Sleep(5 * time.Second)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go loader.watch(ctx, logger)

	proxy := httputil.NewSingleHostReverseProxy(backend)
	proxy.FlushInterval = -1
	proxy.ErrorLog = logger
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Printf("proxy error for %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	server := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          logger,
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: store.getCertificate,
		},
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Printf("listening on %s and proxying to %s", cfg.listenAddr, backend.Redacted())
		serverErrors <- server.ListenAndServeTLS("", "")
	}()

	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Printf("graceful shutdown failed: %v", err)
		}
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("TLS server failed: %v", err)
		}
	}
}

func loadConfig() (config, error) {
	pollInterval := defaultPollInterval
	if value := strings.TrimSpace(os.Getenv("CERT_POLL_INTERVAL")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return config{}, fmt.Errorf("invalid CERT_POLL_INTERVAL")
		}
		pollInterval = parsed
	}

	cfg := config{
		listenAddr:      valueOrDefault("LISTEN_ADDR", defaultListenAddr),
		backendURL:      valueOrDefault("BACKEND_URL", defaultBackendURL),
		p12Path:         strings.TrimSpace(os.Getenv("PLEX_P12_PATH")),
		preferencesPath: strings.TrimSpace(os.Getenv("PLEX_PREFERENCES_PATH")),
		hostname:        strings.TrimSpace(os.Getenv("PLEX_TLS_HOSTNAME")),
		pollInterval:    pollInterval,
	}
	if cfg.p12Path == "" || cfg.preferencesPath == "" || cfg.hostname == "" {
		return config{}, errors.New("PLEX_P12_PATH, PLEX_PREFERENCES_PATH, and PLEX_TLS_HOSTNAME are required")
	}
	return cfg, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func (l *certificateLoader) watch(ctx context.Context, logger *log.Logger) {
	ticker := time.NewTicker(l.config.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := l.refresh(); err != nil {
				logger.Printf("certificate refresh failed; retaining current certificate: %v", err)
			}
		}
	}
}

func (l *certificateLoader) refresh() error {
	p12Data, err := os.ReadFile(l.config.p12Path)
	if err != nil {
		return fmt.Errorf("read PKCS#12 bundle: %w", err)
	}
	digest := sha256.Sum256(p12Data)
	if l.hasLastGood && digest == l.lastDigest {
		return nil
	}

	preferenceData, err := os.ReadFile(l.config.preferencesPath)
	if err != nil {
		return fmt.Errorf("read PMS preferences: %w", err)
	}
	processedID, err := parseProcessedMachineIdentifier(preferenceData)
	if err != nil {
		return err
	}

	candidate, err := decodeAndValidate(p12Data, derivePassword(processedID), l.config.hostname, time.Now(), l.roots)
	if err != nil {
		return err
	}

	l.store.active.Store(candidate)
	l.lastDigest = digest
	l.hasLastGood = true
	return nil
}

func parseProcessedMachineIdentifier(data []byte) (string, error) {
	var parsed preferences
	if err := xml.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("parse PMS preferences: %w", err)
	}
	processedID := strings.TrimSpace(parsed.ProcessedMachineIdentifier)
	if processedID == "" {
		return "", errors.New("ProcessedMachineIdentifier is missing from PMS preferences")
	}
	return processedID, nil
}

func derivePassword(processedID string) string {
	digest := sha512.Sum512([]byte("plex" + processedID))
	return hex.EncodeToString(digest[:])
}

func decodeAndValidate(p12Data []byte, password, hostname string, now time.Time, roots *x509.CertPool) (*tls.Certificate, error) {
	privateKey, leaf, chain, err := pkcs12.DecodeChain(p12Data, password)
	if err != nil {
		return nil, fmt.Errorf("decode PKCS#12 bundle: %w", err)
	}
	if leaf == nil {
		return nil, errors.New("PKCS#12 bundle has no leaf certificate")
	}
	if len(chain) == 0 {
		return nil, errors.New("PKCS#12 bundle has no certificate chain")
	}
	if err := validateDecodedCertificate(privateKey, leaf, chain, hostname, now, roots); err != nil {
		return nil, err
	}

	certificateBytes := make([][]byte, 0, len(chain)+1)
	certificateBytes = append(certificateBytes, leaf.Raw)
	for _, certificate := range chain {
		certificateBytes = append(certificateBytes, certificate.Raw)
	}
	return &tls.Certificate{
		Certificate: certificateBytes,
		PrivateKey:  privateKey,
		Leaf:        leaf,
	}, nil
}

func validateDecodedCertificate(privateKey crypto.PrivateKey, leaf *x509.Certificate, chain []*x509.Certificate, hostname string, now time.Time, roots *x509.CertPool) error {
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return errors.New("leaf certificate is not currently valid")
	}
	if err := leaf.VerifyHostname(hostname); err != nil {
		return fmt.Errorf("leaf certificate does not cover configured hostname: %w", err)
	}
	if err := verifyKeyPair(privateKey, leaf); err != nil {
		return err
	}

	if roots == nil {
		systemRoots, err := x509.SystemCertPool()
		if err != nil {
			return fmt.Errorf("load system trust store: %w", err)
		}
		roots = systemRoots
	}
	intermediates := x509.NewCertPool()
	for _, certificate := range chain {
		intermediates.AddCert(certificate)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:       hostname,
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
	}); err != nil {
		return fmt.Errorf("verify certificate chain: %w", err)
	}
	return nil
}

func verifyKeyPair(privateKey crypto.PrivateKey, leaf *x509.Certificate) error {
	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		return errors.New("PKCS#12 private key cannot sign")
	}
	privatePublic, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return fmt.Errorf("marshal private-key public component: %w", err)
	}
	leafPublic, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal certificate public key: %w", err)
	}
	if !bytes.Equal(privatePublic, leafPublic) {
		return errors.New("leaf certificate and private key do not match")
	}
	return nil
}
