package factorysupply

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func policyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FACTORY_SHOP_ID", "shop_test")
	file := filepath.Join(t.TempDir(), "supply-policy.json")
	if err := os.WriteFile(file, []byte(`{"schema_version":1,"shop_id":"shop_test","main_origin":"https://coco9.cc","main_connection_id":7,"reserved_brands":["ChatGPT","MainBrand"],"main_products":{"11":"ChatGPT"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_SUPPLY_POLICY_FILE", file)
}

func TestPolicyLocalBrandsAndReservedAliases(t *testing.T) {
	policyEnv(t)
	p, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ChatGPT", "ＣｈａｔＧＰＴ", "chat-gpt", "Chat\u200bGPT", "GPT Plus", "OpenAI", "MainBrand"} {
		if err := p.ValidateMerchant("Independent", name); !errors.Is(err, ErrMainSupplyRequired) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	if err := p.ValidateMerchant("Independent", "独立品牌自营商品"); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateMerchant("", "匿名商品"); !errors.Is(err, ErrBrandRequired) {
		t.Fatal(err)
	}
}

func TestPolicyPinsImportAndExecution(t *testing.T) {
	policyEnv(t)
	p, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if brand, source, err := p.Import(7, "https://coco9.cc", 11, "ChatGPT Plus"); err != nil || brand != "ChatGPT" || source != Main {
		t.Fatalf("%s %s %v", brand, source, err)
	}
	for _, args := range []struct {
		id      uint
		origin  string
		product uint
		text    string
	}{
		{8, "https://other.example", 11, "ChatGPT"}, {7, "https://coco9.cc.evil.example", 11, "ChatGPT"}, {7, "https://coco9.cc", 12, "ChatGPT"},
	} {
		if _, _, err := p.Import(args.id, args.origin, args.product, args.text); err == nil {
			t.Fatalf("accepted %+v", args)
		}
	}
	if err := p.CheckConnection(7, "https://else.example", "dujiao-next"); err == nil {
		t.Fatal("repoint accepted")
	}
	if err := p.CheckRoute(Main, 8); err == nil {
		t.Fatal("rerouted main order")
	}
	if err := p.CheckRoute(Main, 7); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyMissingInvalidAndWrongShopFailClosed(t *testing.T) {
	policyEnv(t)
	file := os.Getenv("FACTORY_SUPPLY_POLICY_FILE")
	for _, body := range []string{"{}", `{"schema_version":1,"shop_id":"other","main_origin":"https://coco9.cc","reserved_brands":["ChatGPT"]}`, "{"} {
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("missing policy accepted")
	}
	t.Setenv("FACTORY_SHOP_ID", "")
	if p, err := Load(); err != nil || p != nil {
		t.Fatalf("original site affected: %v", err)
	}
}
