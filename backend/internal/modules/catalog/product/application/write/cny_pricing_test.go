package productwrite

import (
	"github.com/shopspring/decimal"
	"testing"
)

func TestCNYPriceRoundingAndValidation(t *testing.T) {
	for _, tc := range []struct {
		cny, want string
		invalid   bool
	}{{"3.355", "0.50", false}, {"48.004", "7.15", false}, {"-0.001", "", true}, {"0.001", "", true}} {
		cny := decimal.RequireFromString(tc.cny)
		price, _, err := resolveCatalogPrice(decimal.NewFromInt(999), &cny, decimal.Zero)
		if tc.invalid {
			if err == nil {
				t.Fatalf("accepted invalid %s", tc.cny)
			}
			continue
		}
		if err != nil || price.StringFixed(2) != tc.want {
			t.Fatalf("CNY %s: %s, %v", tc.cny, price, err)
		}
	}
}
