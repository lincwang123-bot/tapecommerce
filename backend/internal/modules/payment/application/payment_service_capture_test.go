package application

import (
	context "context"
	json "encoding/json"
	errors "errors"
	constants "github.com/dujiao-next/internal/constants"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	nowpayments "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/nowpayments"
	provider "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/provider"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	jsonmap "github.com/dujiao-next/internal/shared/jsonmap"
	io "io"
	http "net/http"
	strings "strings"
	testing "testing"
)

// recordingCapturer 捕获传入 ValidateConfig 的第二参数,用于回归测试。
type recordingCapturer struct {
	receivedMode string
}

func (r *recordingCapturer) Type() string { return "test_capturer" }

func (r *recordingCapturer) ValidateConfig(_ jsonmap.JSON, mode string) error {
	r.receivedMode = mode
	return errors.New("stop-test")
}

func (r *recordingCapturer) CreatePayment(_ context.Context, _ jsonmap.JSON, _ paymentcontract.GatewayCreateInput) (*paymentcontract.GatewayCreateResult, error) {
	return nil, errors.New("not-implemented")
}

func (r *recordingCapturer) QueryPayment(_ context.Context, _ jsonmap.JSON, _ string) (*paymentcontract.GatewayQueryResult, error) {
	return nil, errors.New("not-implemented")
}

// 防止重新引入 stripe/wechat capture 全部失败的回归:
// captureViaRegistry 必须把 channel.InteractionMode(而非 channel.ChannelType)
// 传给 Capturer.ValidateConfig,否则 stripe/wechat adapter 会判定 mode 非法。
func TestCaptureViaRegistry_PassesInteractionModeNotChannelType(t *testing.T) {
	cap := &recordingCapturer{}
	reg := provider.NewRegistry()
	reg.Register("test_provider", "test_channel", cap)

	svc := &PaymentService{paymentProviderRegistry: reg}

	channel := &paymentdomain.PaymentChannel{
		ID:              1,
		ProviderType:    "test_provider",
		ChannelType:     "test_channel",
		InteractionMode: constants.PaymentInteractionQR,
	}
	payment := &paymentdomain.Payment{
		ID:          1,
		ChannelID:   1,
		ProviderRef: "ref-123",
	}

	_, _ = svc.captureViaRegistry(CapturePaymentInput{}, payment, channel)

	if cap.receivedMode != channel.InteractionMode {
		t.Fatalf("ValidateConfig received mode=%q, want %q (channel.InteractionMode)",
			cap.receivedMode, channel.InteractionMode)
	}
	if cap.receivedMode == channel.ChannelType {
		t.Fatalf("regression: ValidateConfig received channel.ChannelType=%q", cap.receivedMode)
	}
}

type captureNowTransport func(*http.Request) (*http.Response, error)

func (f captureNowTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the public capture entry, the real NOWPayments parser and the existing
// wallet transaction together. No network connections are opened.
func TestCaptureNowPaymentsVerifiedPaymentCreditsOnce(t *testing.T) {
	for _, scenario := range []string{"finished", "pending", "wrong_order", "wrong_ref", "wrong_coin", "underpaid", "wrong_amount", "wrong_channel", "missing_order", "other_provider"} {
		t.Run(scenario, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
			payment.ProviderType, payment.ChannelType, payment.Currency = "nowpayments", "usdttrc20", "USDT"
			payment.GatewayOrderNo = recharge.RechargeNo
			if scenario == "missing_order" {
				payment.GatewayOrderNo = ""
			}
			if err := db.Save(payment).Error; err != nil {
				t.Fatal(err)
			}
			recharge.ProviderType, recharge.ChannelType, recharge.Currency = "nowpayments", "usdttrc20", "USDT"
			if err := db.Save(recharge).Error; err != nil {
				t.Fatal(err)
			}
			channel := &paymentdomain.PaymentChannel{ID: payment.ChannelID, ProviderType: "nowpayments", ChannelType: "usdttrc20", InteractionMode: "qr", ConfigJSON: jsonmap.JSON{"api_key": "mock-only", "ipn_secret": "mock-only-secret-long-enough", "ipn_callback_url": "https://rehearsal.invalid/ipn"}}
			if scenario == "wrong_channel" {
				channel.ChannelType = "other"
			}
			if scenario == "other_provider" {
				channel.ProviderType = "epay"
			}
			if err := db.Create(channel).Error; err != nil {
				t.Fatal(err)
			}
			body := map[string]string{"payment_id": payment.ProviderRef, "order_id": recharge.RechargeNo, "payment_status": "finished", "pay_amount": "88.00", "actually_paid": "88.00", "pay_currency": "usdttrc20"}
			switch scenario {
			case "pending":
				body["payment_status"] = "confirming"
			case "wrong_order":
				body["order_id"] = "another-order"
			case "wrong_ref":
				body["payment_id"] = "another-payment"
			case "wrong_coin":
				body["pay_currency"] = "btc"
			case "underpaid":
				body["actually_paid"] = "87.99"
			case "wrong_amount":
				body["pay_amount"], body["actually_paid"] = "89.00", "89.00"
			}
			calls := 0
			adapter := nowpayments.New()
			adapter.Client = &http.Client{Transport: captureNowTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/"+payment.ProviderRef) {
					t.Fatal("unexpected gateway request")
				}
				b, _ := json.Marshal(body)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: http.Header{}}, nil
			})}
			reg := provider.NewRegistry()
			reg.Register("nowpayments", "", adapter)
			svc.paymentProviderRegistry = reg
			got, err := svc.CapturePayment(CapturePaymentInput{PaymentID: payment.ID})
			if scenario == "finished" {
				if err != nil || got.Status != constants.PaymentStatusSuccess {
					t.Fatalf("capture not successful: %v", err)
				}
				if _, err = svc.CapturePayment(CapturePaymentInput{PaymentID: payment.ID}); err != nil {
					t.Fatal(err)
				}
				assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)
				var count int64
				db.Model(&walletdomain.Transaction{}).Count(&count)
				if count != 1 || calls != 1 {
					t.Fatalf("duplicate credit or query: ledger=%d queries=%d", count, calls)
				}
			} else {
				if scenario == "pending" {
					if err != nil || got.Status == constants.PaymentStatusSuccess {
						t.Fatalf("pending query: %v", err)
					}
				} else if err == nil {
					t.Fatal("unverified payment accepted")
				}
				var count int64
				db.Model(&walletdomain.Transaction{}).Count(&count)
				if count != 0 {
					t.Fatal("unverified payment credited")
				}
				if (scenario == "wrong_channel" || scenario == "other_provider") && calls != 0 {
					t.Fatal("unsupported channel queried")
				}
			}
		})
	}
}
