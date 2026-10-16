package selfhost

import (
	"context"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	"testing"
)

func TestClosedNetworkRegistryAndAddressFamilies(t *testing.T) {
	v := New()
	for _, test := range []struct {
		channel, network string
		decimals         uint8
		chain            uint64
	}{{"usdtbep20", "bsc", 18, 56}, {"usdttrc20", "tron", 6, 0}, {"usdtpolygon", "polygon", 6, 137}, {"usdterc20", "ethereum", 6, 1}} {
		n, ok := v.Network(test.channel)
		if !ok || n.Network != test.network || n.Decimals != test.decimals || n.ChainID != test.chain {
			t.Fatal(n)
		}
	}
	if _, ok := v.Network("USDT"); ok {
		t.Fatal("ambiguous channel accepted")
	}
	if _, ok := v.NormalizeAddress("tron", "0x1111111111111111111111111111111111111111"); ok {
		t.Fatal("EVM address accepted on TRON")
	}
	if _, ok := v.NormalizeAddress("bsc", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"); ok {
		t.Fatal("TRON address accepted on EVM")
	}
	if _, e := v.ReadHead(context.Background(), "unknown"); e == nil || e.Code != "unsupported_network" {
		t.Fatal(e)
	}
	if _, e := v.Verify(context.Background(), "unknown", paymentcontract.ChainVerifyRequest{}); e == nil || e.Code != "unsupported_network" {
		t.Fatal(e)
	}
}
