package domain

import "time"

// ChainConfirmationGrace never extends the immutable transfer deadline. It only
// reserves time to observe finality for a transfer included before that deadline.
const ChainConfirmationGrace = 30 * time.Minute

// PendingChainConfirmationUntil accepts only an existing reserved selfhost
// invoice belonging to this still-pending payment. Caller locks its business
// order/payment before using the result to defer automatic cancellation.
func PendingChainConfirmationUntil(p *Payment, v *ChainIntent, now time.Time) (time.Time, bool) {
	if p == nil || v == nil || p.ProviderType != "selfhost" || p.Status != "pending" || p.Currency != "USDT" ||
		p.ID != v.PaymentID || p.OrderID != v.OrderID || p.SupersededAt != nil || p.PaidAt != nil ||
		v.CreatedAt.IsZero() || !v.ExpiresAt.After(v.CreatedAt) || v.CreatedAt.After(now) ||
		v.SettlementAmount != p.Amount.String() || !p.Amount.Decimal.IsPositive() ||
		v.Recipient == "" || v.TokenContract == "" || v.ExpectedUnits == "" || v.MinBlockNumber == 0 {
		return time.Time{}, false
	}
	networks := map[string]string{"usdtbep20": "bsc", "usdtpolygon": "polygon", "usdttrc20": "tron", "usdterc20": "ethereum"}
	if networks[p.ChannelType] == "" || networks[p.ChannelType] != v.Network {
		return time.Time{}, false
	}
	switch v.VerificationStatus {
	case "awaiting_hash", "pending", "unavailable", "rejected":
	default:
		return time.Time{}, false
	}
	until := v.ExpiresAt.Add(ChainConfirmationGrace)
	return until, now.Before(until)
}
