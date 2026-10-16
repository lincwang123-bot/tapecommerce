package domain

import (
	"strings"
	"testing"
)

func TestInvoiceRejectsAmbiguousAndOverflowAmounts(t *testing.T) {
	i := Invoice{InvoiceID: "0x" + strings.Repeat("1", 64), ShopID: "one", OrderID: 1, ChainID: 196, Checkout: "0x" + strings.Repeat("2", 40), TenantHash: "0x" + strings.Repeat("3", 64), Payer: "0x" + strings.Repeat("4", 40), Merchant: "0x" + strings.Repeat("5", 40), AmountWei: "1", Deadline: 61, FXSnapshot: FXSnapshot{PolicyVersion: FXPolicyVersion, Source: FXSource, InstrumentID: FXInstrument, PriceField: FXPriceField, RateUSDTPerOKB: "999999999999999999", AmountUSDT: "0.01", SourceTimestampMS: 1000, FetchedAtMS: 1000, IssuedAtMS: 1000, Rounding: FXRounding}}
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, amount := range []string{"0", "-1", "+1", "01", "1.5", "1e18", strings.Repeat("9", 79), "115792089237316195423570985008687907853269984665640564039457584007913129639936"} {
		bad := i
		bad.AmountWei = amount
		if bad.Validate() == nil {
			t.Errorf("accepted %q", amount)
		}
	}
	i.Merchant = i.Checkout
	if i.Validate() == nil {
		t.Fatal("merchant cannot be checkout")
	}
}
