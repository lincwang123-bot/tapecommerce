package selfhost

import (
	"context"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	"testing"
)

func TestProductionVerifierExposesSeparateDiscoveryPort(t *testing.T) {
	d, ok := New().(paymentcontract.ChainDiscoverer)
	if !ok {
		t.Fatal("production verifier silently disabled automatic discovery")
	}
	if p, e := d.Discover(context.Background(), "unknown", paymentcontract.ChainDiscoverRequest{}); p != nil || e == nil || e.Code != "unsupported_network" {
		t.Fatal(p, e)
	}
}
