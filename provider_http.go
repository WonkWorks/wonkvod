package main

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const providerProxyEnv = "WONKVOD_PROVIDER_PROXY_URL"

// providerHTTPTransport returns a transport dedicated to provider-derived
// traffic. An explicitly configured proxy is applied with ProxyURL so a proxy
// failure cannot fall back to a direct provider connection.
func providerHTTPTransport() (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	value := strings.TrimSpace(os.Getenv(providerProxyEnv))
	if value == "" {
		return transport, nil
	}
	proxyURL, err := url.Parse(value)
	if err != nil || proxyURL.Scheme != "http" || proxyURL.Host == "" || proxyURL.Fragment != "" {
		return nil, errors.New("WONKVOD_PROVIDER_PROXY_URL must be an absolute HTTP URL")
	}
	transport.Proxy = http.ProxyURL(proxyURL)
	return transport, nil
}

func providerHTTPClient(transport http.RoundTripper, timeout time.Duration) *http.Client {
	return &http.Client{Transport: transport, Timeout: timeout}
}
