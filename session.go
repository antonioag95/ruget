package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// newSession builds the HTTP client ruTorrent expects: TLS verification is
// intentionally disabled (matching the Python original's verify=False).
func newSession() *http.Client {
	dialer := &net.Dialer{Timeout: dialTimeout}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: responseHeaderWait,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       60 * time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // parity with scarica.py
	}

	return &http.Client{Transport: transport}
}

// setCommonHeaders mirrors the headers scarica.py sends with every request.
func setCommonHeaders(req *http.Request, base string) {
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Referer", base)
	req.Header.Set("Origin", base)
}
