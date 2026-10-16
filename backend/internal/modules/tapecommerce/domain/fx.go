package domain

import (
	"math/big"
	"regexp"
	"strings"
	"time"
)

const (
	FXPolicyVersion               = "okx_spot_last_v1"
	FXSource                      = "okx"
	FXInstrument                  = "OKB-USDT"
	FXPriceField                  = "last"
	FXRounding                    = "ceil_wei"
	FXMaxAgeMS             uint64 = 30_000
	FXMaxFutureMS          uint64 = 5_000
	FXQuoteLifetimeSeconds uint64 = 60
	MaxSafeInteger         uint64 = 9_007_199_254_740_991
)

var (
	rateDecimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,18})?$`)
	usdtAmountPattern  = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})\.[0-9]{2}$`)
)

// FXSnapshot is an immutable audit record of the server's public OKX spot
// ticker observation. Strings retain decimal precision and the exact last
// value, including trailing zeros. Timestamps use milliseconds, never seconds.
// These fields are not additional EIP-712 fields: their immutable stored values
// must reproduce the signed AmountWei and Deadline exactly.
type FXSnapshot struct {
	PolicyVersion     string `json:"policyVersion"`
	Source            string `json:"source"`
	InstrumentID      string `json:"instrumentId"`
	PriceField        string `json:"priceField"`
	RateUSDTPerOKB    string `json:"rateUSDTPerOKB"`
	SourceTimestampMS uint64 `json:"sourceTimestampMs"`
	FetchedAtMS       uint64 `json:"fetchedAtMs"`
	AmountUSDT        string `json:"amountUSDT"`
	Rounding          string `json:"rounding"`
	SpreadBPS         uint32 `json:"spreadBps"`
	IssuedAtMS        uint64 `json:"issuedAtMs"`
}

// QuoteAmountWei validates the observation at fetch/issue time and uses only
// integer arithmetic: ceil(cents * 10^(rateDecimals+16) / rateInteger).
func (f FXSnapshot) QuoteAmountWei() (string, error) {
	if f.PolicyVersion != FXPolicyVersion || f.Source != FXSource || f.InstrumentID != FXInstrument || f.PriceField != FXPriceField || f.Rounding != FXRounding || f.SpreadBPS != 0 {
		return "", ErrInvalid
	}
	if !safeTimestamp(f.SourceTimestampMS) || !safeTimestamp(f.FetchedAtMS) || !safeTimestamp(f.IssuedAtMS) || f.FetchedAtMS > f.IssuedAtMS {
		return "", ErrInvalid
	}
	if !freshAt(f.SourceTimestampMS, f.FetchedAtMS) || !freshAt(f.SourceTimestampMS, f.IssuedAtMS) {
		return "", ErrInvalid
	}
	// Bound strings before parsing or exponentiation; never accept exponent,
	// sign, whitespace, float rounding, or more precision than the policy.
	if len(f.RateUSDTPerOKB) > 37 || !rateDecimalPattern.MatchString(f.RateUSDTPerOKB) || len(f.AmountUSDT) > 21 || !usdtAmountPattern.MatchString(f.AmountUSDT) {
		return "", ErrInvalid
	}
	rateParts := strings.SplitN(f.RateUSDTPerOKB, ".", 2)
	rateDigits, scale := rateParts[0], 0
	if len(rateParts) == 2 {
		rateDigits += rateParts[1]
		scale = len(rateParts[1])
	}
	rate, ok := new(big.Int).SetString(rateDigits, 10)
	if !ok || rate.Sign() <= 0 {
		return "", ErrInvalid
	}
	cents, ok := new(big.Int).SetString(strings.ReplaceAll(f.AmountUSDT, ".", ""), 10)
	if !ok || cents.Sign() <= 0 {
		return "", ErrInvalid
	}
	power := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale+16)), nil)
	numerator := new(big.Int).Mul(cents, power)
	wei, remainder := new(big.Int), new(big.Int)
	wei.QuoRem(numerator, rate, remainder)
	if remainder.Sign() != 0 {
		wei.Add(wei, big.NewInt(1))
	}
	if wei.Sign() <= 0 || wei.BitLen() > 256 {
		return "", ErrInvalid
	}
	return wei.String(), nil
}

// ValidateBinding is independent of wall clock. A receipt mined before the
// quote deadline must still be verifiable after safe confirmations arrive.
func (f FXSnapshot) ValidateBinding(amountWei string, deadline uint64) error {
	computed, err := f.QuoteAmountWei()
	if err != nil {
		return err
	}
	if amountWei != computed || deadline != f.IssuedAtMS/1000+FXQuoteLifetimeSeconds {
		return ErrInvalid
	}
	return nil
}

// ValidateAtIssue is for accepting a newly issued quote, not payment receipt
// verification. It prevents delayed/future quote admission using current time.
func (f FXSnapshot) ValidateAtIssue(now time.Time) error {
	if _, err := f.QuoteAmountWei(); err != nil {
		return err
	}
	ms := now.UnixMilli()
	if ms <= 0 || uint64(ms) > MaxSafeInteger {
		return ErrInvalid
	}
	at := uint64(ms)
	// The 5-second skew allowance belongs only to the exchange timestamp.
	// Local observation/issuance timestamps may not be in the future.
	if !freshAt(f.SourceTimestampMS, at) || f.IssuedAtMS > at {
		return ErrInvalid
	}
	return nil
}

func safeTimestamp(ms uint64) bool { return ms > 0 && ms <= MaxSafeInteger }
func freshAt(source, at uint64) bool {
	if source > at {
		return source-at <= FXMaxFutureMS
	}
	return at-source <= FXMaxAgeMS
}
