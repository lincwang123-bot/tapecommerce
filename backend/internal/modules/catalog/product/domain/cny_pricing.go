package productdomain

import "encoding/json"

// CNYPerUSDT is the fixed catalog conversion rate; all settlement stays in USDT.
const CNYPerUSDT = "6.71"

func (p Product) MarshalJSON() ([]byte, error) {
	type alias Product
	return json.Marshal(struct {
		alias
		CNYPerUSDT string `json:"cny_per_usdt"`
	}{alias(p), CNYPerUSDT})
}
