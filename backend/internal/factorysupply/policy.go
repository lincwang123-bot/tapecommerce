// Package factorysupply enforces the host-owned supply policy for managed shops.
// A merchant's product labels never grant access to the main supply connection.
package factorysupply

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const Main = "main"
const Merchant = "merchant"

var (
	ErrPolicyUnavailable  = errors.New("分站供货规则未就绪，请联系主站")
	ErrMainSupplyRequired = errors.New("该品牌必须由主站供货和履约，请从主站商品库导入")
	ErrBrandRequired      = errors.New("请填写真实品牌；品牌归属保存后不可自行变更")
	ErrManagedSupply      = errors.New("主站供货来源由平台管理，分站不可更换或解除绑定")
)

type Policy struct {
	SchemaVersion    int               `json:"schema_version"`
	ShopID           string            `json:"shop_id"`
	MainOrigin       string            `json:"main_origin"`
	MainConnectionID uint              `json:"main_connection_id"`
	ReservedBrands   []string          `json:"reserved_brands"`
	MainProducts     map[string]string `json:"main_products"`
}

// Load does not cache: an unreadable or changed host policy must not leave a
// permissive cached copy in a web process or a background worker.
func Load() (*Policy, error) {
	shop := os.Getenv("FACTORY_SHOP_ID")
	if shop == "" {
		return nil, nil
	}
	f, err := os.Open(os.Getenv("FACTORY_SUPPLY_POLICY_FILE"))
	if err != nil {
		return nil, ErrPolicyUnavailable
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return nil, ErrPolicyUnavailable
	}
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	var p Policy
	if err := dec.Decode(&p); err != nil {
		return nil, ErrPolicyUnavailable
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || p.SchemaVersion != 1 || p.ShopID != shop || p.MainOrigin != "https://coco9.cc" {
		return nil, ErrPolicyUnavailable
	}
	for id, brand := range p.MainProducts {
		n, err := strconv.ParseUint(id, 10, 64)
		if err != nil || n == 0 || strings.TrimSpace(brand) == "" || p.MainConnectionID == 0 {
			return nil, ErrPolicyUnavailable
		}
	}
	return &p, nil
}

func normalized(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, norm.NFKC.String(s))
}

// Text checks catch common aliases and obfuscation; they cannot establish what
// an intentionally mislabelled product really delivers. Merchant review remains
// necessary. Existing managed identity survives all product renames.
func (p *Policy) Reserved(text ...any) bool {
	if p == nil {
		return false
	}
	aliases := []string{"chatgpt", "openai", "gptplus", "gptpro", "gptteam", "gptbusiness", "gpt4", "gpt5", "gpt6"}
	aliases = append(aliases, p.ReservedBrands...)
	for _, brand := range p.MainProducts {
		aliases = append(aliases, brand)
	}
	for _, v := range text {
		raw, _ := json.Marshal(v)
		hay := normalized(string(raw))
		for _, a := range aliases {
			needle := normalized(a)
			if needle != "" && strings.Contains(hay, needle) {
				return true
			}
		}
	}
	return false
}

func (p *Policy) ValidateMerchant(brand string, text ...any) error {
	if p == nil {
		return nil
	}
	if strings.TrimSpace(brand) == "" || len(brand) > 100 {
		return ErrBrandRequired
	}
	if p.Reserved(append([]any{brand}, text...)...) {
		return ErrMainSupplyRequired
	}
	return nil
}

func (p *Policy) ValidateProduct(brand, source, delivery string, text ...any) error {
	if p == nil {
		return nil
	}
	if source == Main {
		if brand == "" || delivery != "upstream" {
			return ErrMainSupplyRequired
		}
		return nil
	}
	if source != Merchant {
		return ErrBrandRequired
	}
	return p.ValidateMerchant(brand, text...)
}

// Import trusts only the operator's connection and catalog IDs, never a
// merchant-supplied brand or the name returned by a third-party supplier.
func (p *Policy) Import(connectionID uint, origin string, productID uint, text ...any) (string, string, error) {
	if p == nil {
		return "", "", nil
	}
	if err := p.CheckConnection(connectionID, origin, "dujiao-next"); err != nil {
		return "", "", err
	}
	if connectionID == p.MainConnectionID && connectionID != 0 {
		brand := p.MainProducts[strconv.FormatUint(uint64(productID), 10)]
		if brand == "" {
			return "", "", ErrMainSupplyRequired
		}
		return brand, Main, nil
	}
	if p.Reserved(text...) {
		return "", "", ErrMainSupplyRequired
	}
	// Third-party imports remain drafts until the merchant assigns their brand.
	return "", Merchant, nil
}

func (p *Policy) CheckConnection(id uint, origin, protocol string) error {
	if p != nil && id != 0 && id == p.MainConnectionID && (strings.TrimRight(origin, "/") != p.MainOrigin || protocol != "dujiao-next") {
		return ErrManagedSupply
	}
	return nil
}
func (p *Policy) CheckRoute(source string, connectionID uint) error {
	if p != nil && source == Main && (p.MainConnectionID == 0 || connectionID != p.MainConnectionID) {
		return ErrManagedSupply
	}
	return nil
}
func (p *Policy) MutableConnection(id uint) error {
	if p != nil && id != 0 && id == p.MainConnectionID {
		return ErrManagedSupply
	}
	return nil
}
func Error(err error) bool {
	return errors.Is(err, ErrPolicyUnavailable) || errors.Is(err, ErrMainSupplyRequired) || errors.Is(err, ErrBrandRequired) || errors.Is(err, ErrManagedSupply)
}
func LockedBrand(previous, next string) error {
	if previous != "" && next != "" && previous != next {
		return fmt.Errorf("%w", ErrBrandRequired)
	}
	return nil
}
