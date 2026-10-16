package callbackclient

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	downstreamcontract "github.com/dujiao-next/internal/modules/downstreamcallback/contract"
	"github.com/dujiao-next/internal/upstream"
)

func TestClientSendsSignedCallback(t *testing.T) {
	const secret = "downstream-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read callback body: %v", err)
			return
		}
		if request.Method != http.MethodPost || request.Header.Get(upstream.HeaderApiKey) != "downstream-key" {
			t.Errorf("unexpected request: method=%s api_key=%q", request.Method, request.Header.Get(upstream.HeaderApiKey))
		}
		timestamp, err := strconv.ParseInt(request.Header.Get(upstream.HeaderTimestamp), 10, 64)
		if err != nil {
			t.Errorf("parse timestamp: %v", err)
			return
		}
		wantSignature := upstream.Sign(secret, http.MethodPost, signaturePath, timestamp, body)
		if request.Header.Get(upstream.HeaderSignature) != wantSignature {
			t.Errorf("signature mismatch: got=%q want=%q", request.Header.Get(upstream.HeaderSignature), wantSignature)
		}
		var payload downstreamcontract.CallbackPayload
		if err := json.Unmarshal(body, &payload); err != nil || payload.Event != "order.fulfilled" {
			t.Errorf("payload mismatch: payload=%#v err=%v", payload, err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	client := localFixtureClient(t, server)
	err := client.Send(context.Background(), downstreamcontract.DeliveryRequest{
		URL:       "http://8.8.8.8/callback",
		APIKey:    "downstream-key",
		APISecret: secret,
		Payload: downstreamcontract.CallbackPayload{
			Event:     "order.fulfilled",
			OrderID:   8,
			Timestamp: 1_700_000_000,
		},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestClientRejectsNonSuccessContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("upstream unavailable"))
	}))
	t.Cleanup(server.Close)

	err := localFixtureClient(t, server).Send(context.Background(), downstreamcontract.DeliveryRequest{
		URL:     "http://8.8.8.8/callback",
		Payload: downstreamcontract.CallbackPayload{Timestamp: 1_700_000_000},
	})
	if err == nil {
		t.Fatal("Send() should reject non-success response")
	}
}

// This test-only transport maps a syntactically public URL to an isolated
// fixture. Production New() always uses the restricted safehttp transport.
func localFixtureClient(t *testing.T, server *httptest.Server) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &Client{httpClient: &http.Client{Transport: transport}}
}

func TestProductionClientNeverReachesLocalhost(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	for _, host := range []string{"127.0.0.1", "LOCALHOST", "localhost.", "LocalHost.", "[::]", "[::1]", "[::ffff:127.0.0.1]"} {
		t.Run(host, func(t *testing.T) {
			client := New()
			defer client.httpClient.CloseIdleConnections()
			err := client.Send(context.Background(), downstreamcontract.DeliveryRequest{URL: "http://" + host + ":" + port + "/callback"})
			if err == nil {
				t.Fatal("unsafe callback was accepted")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("local service received %d requests", hits.Load())
	}
}

func TestNonSuccessIncludesResponseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("fixture unavailable"))
	}))
	t.Cleanup(server.Close)
	err := localFixtureClient(t, server).Send(context.Background(), downstreamcontract.DeliveryRequest{URL: "http://8.8.8.8/callback"})
	if err == nil || !strings.Contains(err.Error(), "502: fixture unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}
