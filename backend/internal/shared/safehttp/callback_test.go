package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateURL(t *testing.T) {
	for _, raw := range []string{
		"http://LOCALHOST/x", "http://localhost./x", "http://a.localhost/x", "http://[::]/x",
		"http://[::1]/x", "http://[::ffff:127.0.0.1]/x", "http://0.0.0.0", "http://0.1.2.3",
		"http://10.0.0.1", "http://172.16.0.1", "http://192.168.1.1", "http://169.254.169.254",
		"http://100.64.0.1", "http://224.0.0.1", "http://[fc00::1]", "http://[fe80::1%25en0]",
		"http://[64:ff9b::a00:1]", "file:///etc/passwd", "http://user:pass@8.8.8.8", "https://",
	} {
		t.Run(raw, func(t *testing.T) {
			if ValidateURL(raw) == nil {
				t.Fatal("unsafe URL accepted")
			}
		})
	}
	for _, raw := range []string{"http://example.com/callback?a=b", "https://example.com:8443/callback", "https://8.8.8.8", "https://[2606:4700:4700::1111]/"} {
		if err := ValidateURL(raw); err != nil {
			t.Errorf("valid callback %q: %v", raw, err)
		}
	}
}

func TestPublicDialPinsResolvedIPAndRechecksNewConnections(t *testing.T) {
	lookups, dials := 0, 0
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	fixtureError := errors.New("no external network in tests")
	dial := publicDial(lookup, func(_ context.Context, _, address string) (net.Conn, error) {
		dials++
		if address != "8.8.8.8:443" {
			t.Fatalf("did not pin public address: %s", address)
		}
		return nil, fixtureError
	})
	if _, err := dial(context.Background(), "tcp", "callback.example:443"); !errors.Is(err, fixtureError) {
		t.Fatal(err)
	}
	if _, err := dial(context.Background(), "tcp", "callback.example:443"); err == nil {
		t.Fatal("rebound host accepted")
	}
	if dials != 1 || lookups != 2 {
		t.Fatalf("lookups=%d dials=%d", lookups, dials)
	}
}

func TestPrivateAndMixedDNSNeverDial(t *testing.T) {
	for _, addresses := range [][]string{{"10.0.0.1"}, {"::"}, {"8.8.8.8", "192.168.1.1"}, {"::ffff:127.0.0.1"}, {}} {
		lookup := func(context.Context, string, string) ([]netip.Addr, error) {
			var ips []netip.Addr
			for _, address := range addresses {
				ips = append(ips, netip.MustParseAddr(address))
			}
			return ips, nil
		}
		dial := publicDial(lookup, func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("unsafe DNS result reached the dialer")
			return nil, nil
		})
		if _, err := dial(context.Background(), "tcp", "callback.example:80"); err == nil {
			t.Fatalf("accepted %v", addresses)
		}
	}
}

func TestClientBlocksRedirectAndProxy(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { targetHits.Add(1) }))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	client := NewClient(time.Second)
	t.Cleanup(client.CloseIdleConnections)
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.DialTLSContext != nil {
		t.Fatal("proxy or TLS dial bypass enabled")
	}
	// Reach only the isolated first-hop fixture; preserve the production redirect policy.
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, redirect.Listener.Addr().String())
	}
	response, err := client.Post("http://8.8.8.8/callback", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || targetHits.Load() != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestRealLocalhostDNSRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ValidateDestination(ctx, "http://LOCALHOST./callback"); err == nil {
		t.Fatal("localhost accepted")
	}
	if _, err := resolvePublic(ctx, net.DefaultResolver.LookupNetIP, "localhost"); err == nil {
		t.Fatal("private DNS accepted")
	}
}
