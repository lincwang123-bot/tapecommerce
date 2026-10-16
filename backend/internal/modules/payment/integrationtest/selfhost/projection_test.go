package selfhost_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	productstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	userstore "github.com/dujiao-next/internal/modules/identity/user/infrastructure/gormstore"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	app "github.com/dujiao-next/internal/modules/payment/application"
	contract "github.com/dujiao-next/internal/modules/payment/contract"
	realadapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/selfhost"
	store "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	presenter "github.com/dujiao-next/internal/modules/payment/transport/presenter"
	walletapp "github.com/dujiao-next/internal/modules/wallet/application"
	walletstore "github.com/dujiao-next/internal/modules/wallet/infrastructure/gormstore"
	walletpresenter "github.com/dujiao-next/internal/modules/wallet/transport/presenter"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

// Keep the real fixed network/normalization contract. Only chain reads are
// synthetic, so this test can never make an external RPC call.
type projectionVerifier struct{ contract.ChainVerifier }

func (projectionVerifier) ReadHead(context.Context, string) (*contract.ChainHead, *contract.ChainError) {
	return &contract.ChainHead{Number: 100, Hash: "synthetic", Timestamp: time.Now()}, nil
}
func (projectionVerifier) Verify(_ context.Context, _ string, r contract.ChainVerifyRequest) (*contract.ChainEvidence, *contract.ChainError) {
	return &contract.ChainEvidence{TxID: r.TxID, LogIndex: 2, Recipient: r.Recipient, AmountUnits: r.AmountUnits, PaidAt: time.Now(), BlockNumber: 101, FinalizedBlockNumber: 102, BlockHash: "synthetic", Sender: "synthetic"}, nil
}
func syntheticTronAddress() string {
	raw := append([]byte{0x41}, make([]byte, 20)...)
	for i := 1; i < len(raw); i++ {
		raw[i] = 0x11
	}
	first := sha256.Sum256(raw)
	second := sha256.Sum256(first[:])
	raw = append(raw, second[:4]...)
	n := new(big.Int).SetBytes(raw)
	var out []byte
	alphabet := "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	base := big.NewInt(58)
	for n.Sign() > 0 {
		r := new(big.Int)
		n.QuoRem(n, base, r)
		out = append(out, alphabet[r.Int64()])
	}
	for a, b := 0, len(out)-1; a < b; a, b = a+1, b-1 {
		out[a], out[b] = out[b], out[a]
	}
	return string(out)
}
func TestFourNetworksUseRealMetadataAndPresenters(t *testing.T) {
	all := map[string]any{}
	for _, channelType := range []string{"usdtbep20", "usdttrc20", "usdtpolygon", "usdterc20"} {
		t.Run(channelType, func(t *testing.T) {
			f := setup(t)
			v := projectionVerifier{realadapter.New()}
			network, ok := v.Network(channelType)
			if !ok {
				t.Fatal("unsupported fixed network")
			}
			address := "0x" + strings.Repeat("1", 40)
			if network.Network == "tron" {
				address = syntheticTronAddress()
			}
			f.channel.ChannelType = channelType
			f.channel.ConfigJSON = jsonmap.JSON{"receiving_address": address}
			if e := f.db.Save(f.channel).Error; e != nil {
				t.Fatal(e)
			}
			w := walletstore.New(f.db)
			paymentRepo := store.New(f.db, "synthetic-guest-secret-only")
			f.service = app.NewPaymentService(app.PaymentServiceOptions{PaymentStore: paymentRepo, ChannelStore: store.NewChannelStore(f.db), OrderStore: orderstore.New(f.db, "synthetic-guest-secret-only"), ProductRepo: productstore.NewProductStore(f.db), UserStore: userstore.New(f.db), WalletRepo: w, WalletService: walletapp.NewService(walletapp.Options{Repository: w, Transactions: w}), ChainVerifier: v, ExpireMinutes: 15})
			p := f.order(t)
			intent := intentFor(t, f, p.ID)
			if network.Network == "bsc" && intent.Decimals != 18 {
				t.Fatal("BSC decimals")
			}
			if network.Network != "bsc" && intent.Decimals != 6 {
				t.Fatal("token decimals")
			}
			want := decimal.RequireFromString(intent.ChainAmount).Shift(int32(network.Decimals)).StringFixed(0)
			if want != intent.ExpectedUnits {
				t.Fatal("precision truncated")
			}
			created := presenter.NewCreatePaymentResp(&presenter.CreatePaymentResultView{Payment: p, Channel: f.channel})
			latest, e := paymentRepo.GetLatestPendingByOrder(p.OrderID, time.Now())
			if e != nil || latest == nil {
				t.Fatal(e)
			}
			lr := presenter.NewLatestPaymentResp(latest, "")
			r, e := f.service.CreateWalletRechargePayment(app.CreateWalletRechargePaymentInput{UserID: f.user.ID, ChannelID: f.channel.ID, Amount: money.FromDecimal(decimal.NewFromInt(30)), Currency: "USDT"})
			if e != nil {
				t.Fatal(e)
			}
			rp := walletpresenter.NewWalletRechargePaymentPayload(r.Recharge, r.Payment, nil)
			hash := strings.Repeat("a", 64)
			if network.Network != "tron" {
				hash = "0x" + hash
			}
			paid, e := submit(f, p, hash)
			if e != nil {
				t.Fatal(e)
			}
			sr := presenter.NewSubmittedTransactionResp(paid)
			for _, value := range []any{created, lr, rp, sr} {
				raw, e := json.Marshal(value)
				if e != nil {
					t.Fatal(e)
				}
				var projected map[string]any
				json.Unmarshal(raw, &projected)
				if projected["network"] != network.Network || projected["chain"] != strings.ToUpper(network.Network) || projected["token_contract"] != network.TokenContract {
					t.Fatalf("incorrect public contract %s", raw)
				}
				if network.Network == "tron" {
					if _, exists := projected["chain_id"]; exists {
						t.Fatal("TRON must omit EVM chain id")
					}
				}
				if projected["chain_amount"] == "20.00" {
					t.Fatal("chain amount rounded")
				}
			}
			all[channelType] = map[string]any{"create": created, "latest": lr, "recharge": rp, "submit": sr}
		})
	}
	if path := os.Getenv("COCO9_TEST_PROJECTION_OUT"); path != "" {
		if path != "/tmp/coco9-selfhost-projections.json" {
			t.Fatal("non-fixture output path refused")
		}
		raw, e := json.MarshalIndent(all, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, append(raw, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
}
