// Package safehttp restricts untrusted callback destinations to public networks.
package safehttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// ValidateURL validates callback syntax and literal addresses. HTTP is retained
// for the existing upstream API contract; new integrations should use HTTPS.
// DNS is checked separately at acceptance and, critically, at every connection.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		return fmt.Errorf("invalid callback url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("callback url must use http or https")
	}
	if u.User != nil || u.Fragment != "" {
		return fmt.Errorf("callback url must not contain credentials or a fragment")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.Contains(host, "%") {
		return fmt.Errorf("callback url must have a public host")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicIP(ip) {
		return fmt.Errorf("callback url must not point to a non-public network")
	}
	return nil
}

// ValidateDestination checks DNS when accepting an untrusted callback URL.
// This is not a substitute for the pinned connection checks in NewClient.
func ValidateDestination(ctx context.Context, raw string) error {
	if err := ValidateURL(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	_, err := resolvePublic(ctx, net.DefaultResolver.LookupNetIP, u.Hostname())
	return err
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 could otherwise encode private IPv4.
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"), // Teredo transition addresses.
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), // 6to4 can encode private IPv4.
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

type lookupFunc func(context.Context, string, string) ([]netip.Addr, error)
type dialFunc func(context.Context, string, string) (net.Conn, error)

func resolvePublic(ctx context.Context, lookup lookupFunc, host string) ([]netip.Addr, error) {
	host = strings.TrimSuffix(host, ".")
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		var err error
		ips, err = lookup(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("callback host resolution failed: %w", err)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("callback host has no addresses")
	}
	// Reject mixed public/private answers rather than choosing a public answer.
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, fmt.Errorf("callback host resolves to a non-public network")
		}
	}
	return ips, nil
}

func publicDial(lookup lookupFunc, dial dialFunc) dialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := resolvePublic(ctx, lookup, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			// Dial the validated literal, never the hostname: no second DNS lookup.
			conn, dialErr := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			err = dialErr
		}
		return nil, err
	}
}

// NewClient disables proxy routing and redirects, so neither can bypass the
// destination checks or forward callback authentication to a different host.
func NewClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext:         publicDial(net.DefaultResolver.LookupNetIP, dialer.DialContext),
		TLSHandshakeTimeout: 5 * time.Second,
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConns:        20,
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}
