package calling

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPCallbackResult holds the response from an HTTP callback.
type HTTPCallbackResult struct {
	StatusCode int
	Body       string
}

// callbackTarget holds the validated, SSRF-safe components of a callback URL.
// No field originates from unsanitized user input — host is DNS-verified and
// all resolved IPs are confirmed public.
type callbackTarget struct {
	host     string // original hostname (for TLS SNI / Host header)
	port     string // port or "443"
	path     string // URL path
	query    string // raw query string
	publicIP string // first validated public IP
}

// executeHTTPCallback performs an HTTP request with configurable method, headers, and body.
// The URL is validated to prevent SSRF — only HTTPS to public IPs is allowed.
func executeHTTPCallback(callbackURL, method string, headers map[string]string, body string, timeout time.Duration) (*HTTPCallbackResult, error) {
	target, err := validateAndResolve(callbackURL)
	if err != nil {
		return nil, err
	}

	// Build the request URL from validated components. The scheme is always
	// "https" (enforced by validateAndResolve) and the host comes from DNS
	// resolution against a public-IP allowlist.
	reqURL := "https://" + target.host
	if target.port != "443" {
		reqURL += ":" + target.port
	}
	if target.path != "" {
		reqURL += target.path
	}
	if target.query != "" {
		reqURL += "?" + target.query
	}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Pin the connection to the validated public IP so DNS cannot be
	// re-resolved to an internal address between validation and dial (TOCTOU).
	dialAddr := net.JoinHostPort(target.publicIP, target.port)
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				ServerName: target.host, // SNI must match the original host
			},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, dialAddr)
			},
		},
		// Disallow redirects — they could point to internal addresses.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024)) // limit to 64KB
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return &HTTPCallbackResult{
		StatusCode: resp.StatusCode,
		Body:       string(respBody),
	}, nil
}

// validateAndResolve parses the callback URL, enforces HTTPS, resolves the
// hostname, and verifies all IPs are public. Returns a callbackTarget with
// the first validated public IP for connection pinning.
func validateAndResolve(rawURL string) (*callbackTarget, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid callback URL: %w", err)
	}

	if u.Scheme != "https" {
		return nil, fmt.Errorf("callback URL must use HTTPS, got %q", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("callback URL has no host")
	}

	port := u.Port()
	if port == "" {
		port = "443"
	}

	ips, err := net.LookupHost(host)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve callback host %q: %w", host, err)
	}

	var publicIP string
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return nil, fmt.Errorf("callback URL must not point to internal address %s", ipStr)
		}
		if publicIP == "" {
			publicIP = ipStr
		}
	}

	if publicIP == "" {
		return nil, fmt.Errorf("callback host %q has no usable public IP", host)
	}

	return &callbackTarget{
		host:     host,
		port:     port,
		path:     u.Path,
		query:    u.RawQuery,
		publicIP: publicIP,
	}, nil
}

// interpolateTemplate replaces {{key}} placeholders with values from the variables map.
func interpolateTemplate(tpl string, vars map[string]string) string {
	for k, v := range vars {
		tpl = strings.ReplaceAll(tpl, "{{"+k+"}}", v)
	}
	return tpl
}
