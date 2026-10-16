package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

func auditFX() FXSnapshot {
	return FXSnapshot{PolicyVersion: FXPolicyVersion, Source: FXSource, InstrumentID: FXInstrument, PriceField: FXPriceField, RateUSDTPerOKB: "300.0000", SourceTimestampMS: 1_800_000_000_000, FetchedAtMS: 1_800_000_001_000, AmountUSDT: "29.99", Rounding: FXRounding, IssuedAtMS: 1_800_000_002_000}
}

func TestFXExactCeilVectors(t *testing.T) {
	for _, tt := range []struct{ amount, rate, wei string }{
		{"29.99", "300.0000", "99966666666666667"},
		{"0.01", "3", "3333333333333334"},
		{"10.00", "3", "3333333333333333334"},
		{"100.00", "100.000000000000000000", "1000000000000000000"},
		{"0.01", "999999999999999999", "1"},
		{"1.00", "0.000000000000000001", "1000000000000000000000000000000000000"},
		{"999999999999999999.99", "1", "999999999999999999990000000000000000"},
		// Same exact vectors as the Node signer, including amounts beyond 2^53.
		{"117.91", "117.91", "1000000000000000000"},
		{"1.00", "3", "333333333333333334"},
		{"9007199254740993.01", "1", "9007199254740993010000000000000000"},
		{"0.01", "999999999999999999.999999999999999999", "1"},
		{"117.91", "117.91", "1000000000000000000"},
		{"1.00", "3", "333333333333333334"},
		{"9007199254740993.01", "1", "9007199254740993010000000000000000"},
		{"999999999999999999.99", "0.000000000000000001", "999999999999999999990000000000000000000000000000000000"},
	} {
		t.Run(tt.amount+"_"+tt.rate, func(t *testing.T) {
			f := auditFX()
			f.AmountUSDT = tt.amount
			f.RateUSDTPerOKB = tt.rate
			got, err := f.QuoteAmountWei()
			if err != nil || got != tt.wei {
				t.Fatalf("got %s err=%v want %s", got, err, tt.wei)
			}
		})
	}
}

func TestFXPolicyFormatAndTimeBoundaries(t *testing.T) {
	cases := map[string]func(*FXSnapshot){
		"missing_policy": func(f *FXSnapshot) { f.PolicyVersion = "" }, "unknown_policy": func(f *FXSnapshot) { f.PolicyVersion = "v2" },
		"other_source": func(f *FXSnapshot) { f.Source = "other" }, "other_instrument": func(f *FXSnapshot) { f.InstrumentID = "OKB-USD" },
		"wrong_price_field": func(f *FXSnapshot) { f.PriceField = "bidPx" }, "round_down": func(f *FXSnapshot) { f.Rounding = "floor_wei" }, "spread": func(f *FXSnapshot) { f.SpreadBPS = 1 },
		"rate_exponent": func(f *FXSnapshot) { f.RateUSDTPerOKB = "3e2" }, "rate_zero": func(f *FXSnapshot) { f.RateUSDTPerOKB = "0.000" }, "rate_negative": func(f *FXSnapshot) { f.RateUSDTPerOKB = "-300" }, "rate_sign": func(f *FXSnapshot) { f.RateUSDTPerOKB = "+300" },
		"rate_leading_zero": func(f *FXSnapshot) { f.RateUSDTPerOKB = "0300" }, "rate_space": func(f *FXSnapshot) { f.RateUSDTPerOKB = " 300" }, "rate_fraction_overflow": func(f *FXSnapshot) { f.RateUSDTPerOKB = "0." + strings.Repeat("1", 19) }, "rate_whole_overflow": func(f *FXSnapshot) { f.RateUSDTPerOKB = strings.Repeat("1", 19) },
		"amount_no_cents": func(f *FXSnapshot) { f.AmountUSDT = "30" }, "amount_precision": func(f *FXSnapshot) { f.AmountUSDT = "30.001" }, "amount_zero": func(f *FXSnapshot) { f.AmountUSDT = "0.00" }, "amount_overflow": func(f *FXSnapshot) { f.AmountUSDT = strings.Repeat("1", 19) + ".00" },
		"zero_timestamp": func(f *FXSnapshot) { f.SourceTimestampMS = 0 }, "unsafe_js_integer": func(f *FXSnapshot) { f.IssuedAtMS = MaxSafeInteger + 1 }, "fetch_after_issue": func(f *FXSnapshot) { f.FetchedAtMS = f.IssuedAtMS + 1 },
		"stale_at_fetch": func(f *FXSnapshot) { f.SourceTimestampMS = f.FetchedAtMS - 30_001 }, "stale_at_issue": func(f *FXSnapshot) { f.SourceTimestampMS = f.IssuedAtMS - 30_001 }, "future_at_fetch": func(f *FXSnapshot) { f.SourceTimestampMS = f.FetchedAtMS + 5001 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := auditFX()
			change(&f)
			if _, err := f.QuoteAmountWei(); err == nil {
				t.Fatal("accepted invalid FX snapshot")
			}
		})
	}
	f := auditFX()
	f.SourceTimestampMS = f.IssuedAtMS - 30000
	if _, err := f.QuoteAmountWei(); err != nil {
		t.Fatal("30-second boundary rejected", err)
	}
	f = auditFX()
	f.SourceTimestampMS = f.FetchedAtMS + 5000
	if _, err := f.QuoteAmountWei(); err != nil {
		t.Fatal("5-second future boundary rejected", err)
	}
}

func TestFXBindingAndDelayedConfirmation(t *testing.T) {
	f := auditFX()
	f.IssuedAtMS += 999 // EIP-712 deadline uses seconds, not rounded milliseconds.
	wei, _ := f.QuoteAmountWei()
	deadline := f.IssuedAtMS/1000 + 60
	if err := f.ValidateBinding(wei, deadline); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []string{"99966666666666666", "99966666666666668", "099966666666666667"} {
		if f.ValidateBinding(wrong, deadline) == nil {
			t.Fatal("accepted wrong amount", wrong)
		}
	}
	if f.ValidateBinding(wei, deadline+1) == nil || f.ValidateBinding(wei, deadline-1) == nil {
		t.Fatal("accepted changed deadline")
	}
	if f.ValidateAtIssue(time.UnixMilli(int64(f.IssuedAtMS))) != nil {
		t.Fatal("fresh quote rejected")
	}
	if f.ValidateAtIssue(time.UnixMilli(int64(f.SourceTimestampMS+30001))) == nil {
		t.Fatal("stale new quote admitted")
	}
	// Static binding remains valid after the 60-second quote expires: canonical
	// receipt time, not current wall clock, determines whether it paid in time.
	if f.ValidateAtIssue(time.UnixMilli(int64(f.IssuedAtMS+600_000))) == nil {
		t.Fatal("stale issue accepted")
	}
	if err := f.ValidateBinding(wei, deadline); err != nil {
		t.Fatal("historical audit was invalidated", err)
	}
}

func TestFXUsesExistingMoneyStringAndJSONSafeTypes(t *testing.T) {
	f := auditFX()
	f.AmountUSDT = money.FromDecimal(decimal.RequireFromString("29.99")).String()
	wei, err := f.QuoteAmountWei()
	if err != nil || wei != "99966666666666667" {
		t.Fatal(wei, err)
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip FXSnapshot
	if err := json.Unmarshal(b, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip != f {
		t.Fatal("lost audit precision")
	}
	for _, bad := range []string{`{"sourceTimestampMs":1800000000000.5}`, `{"rateUSDTPerOKB":300}`, `{"amountUSDT":29.99}`} {
		var got FXSnapshot
		if json.Unmarshal([]byte(bad), &got) == nil {
			t.Fatal("accepted floating point FX input", bad)
		}
	}
}

func TestFXIssueClockNeverBorrowsExchangeSkewAllowance(t *testing.T) {
	f := auditFX()
	now := time.UnixMilli(int64(f.IssuedAtMS))
	// Future exchange data within the policy window is allowed.
	f.SourceTimestampMS = f.FetchedAtMS + 5000
	if err := f.ValidateAtIssue(now); err != nil {
		t.Fatal("exchange skew should be allowed", err)
	}
	for _, delta := range []uint64{1, 5000} {
		future := f
		future.IssuedAtMS = uint64(now.UnixMilli()) + delta
		future.FetchedAtMS = future.IssuedAtMS
		if err := future.ValidateAtIssue(now); err == nil {
			t.Fatalf("local fetch/issue was allowed %dms in future", delta)
		}
	}
}
