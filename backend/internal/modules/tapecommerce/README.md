# TapeCommerce payment and original-stock bridge

The original-order payment bridge is implemented and exercised on isolated SQLite
with original order creation, payment ledger, reserved card inventory, fulfillment
and authorized claim. Production remains disabled: no live container, public shop
router or production migration invokes it. A zero-value Service/Bridge is disabled,
and DeliverOne still requires an AtomicAdmission implementation.

## Order and payment boundary

`bootstrap/tapecommerce.Bridge` provides private server-to-server Prepare, Persist,
Submit, Status and ClaimDelivery ports. The authenticated Node service supplies the
buyer ID; browser-supplied prices, shop IDs and claimed payment results are never
trusted. Configuration pins shop, checkout, tenant hash, merchant and payment channel.

Prepare pins the current buyer/order/payer. Persist re-reads the original order
under its lock, requires USDT and the exact `money.Amount.String()` payable value,
validates the immutable `okx_spot_last_v1` snapshot and signature shape, then stores
invoice, original pending Payment and signature binding in one transaction. The
signature port is trusted, secret-authenticated server infrastructure; Go does not
verify EIP-712 signatures itself or accept public browser quote submissions. Node
owns signing; the checkout validates the signature on payment.

The snapshot stores OKX OKB-USDT `last`, source/fetch/issue times, original USDT
amount, `ceil_wei`, zero spread and policy version. Integer arithmetic recomputes
`ceil(cents * 10^(rateDecimals+16) / rateInteger)`. Local fetch/issue times cannot
be in the future; only exchange time has 5 seconds skew. Source age is at most
30 seconds on issue/persist. Deadline is floor(issue milliseconds / 1000) + 60.
Historical paid receipts use immutable binding, without re-expiring FX at safe
confirmation. One invoice per order is deliberate: expired quotes require a new
order; historical signed invoices are never overwritten.

The payment admission transaction locks root order, children by ID, payment,
invoice/outbox and inventory. It checks safe evidence, canonical inclusion time,
current lease, buyer, amount, cancellation/refund/prior payment and local-auto
eligibility; then writes the original payment success, original paid order family,
original card allocation/fulfillment and outbox completion together. Stock failure
rolls all those effects back and sends the invoice to review. The original
`CreateAuto` and the bridge share `CreateAutoInTransaction`; card delivery logic
is not duplicated. Post-commit notifications reuse `NotifyAutoCommitted`.

Original pending orders use `OnlinePaidAmount` for their planned online balance;
nonzero is not treated as proof of payment. The bridge requires no wallet portion,
no actual PaidAt, no refund/cancel and no other successful payment. Main/upstream,
manual, reseller, affiliate, hybrid-wallet and fee-bearing orders are unsupported
and fail closed. The generic payment callback rejects TapeCommerce provider
records, so it cannot bypass receipt admission.

Only ClaimDelivery returns decrypted payload, after checking the current buyer,
order/refund state and delivered invoice. Submit/Status return no card payload.
Review after an outbox claim is serialized against actual stock release by the
same invoice row lock. A process mutex or a second RPC check is not used as a
substitute for transactional admission.

## Chain boundary

The verifier requires chain 196, successful direct EOA receipt, exact checkout,
payer and CheckoutPaid event fields, canonical block hash and inclusion between
quote issue and deadline. Two different configured RPC URLs must agree on the
receipt and inclusion timestamp; both safe heads must cover the payment. It also
checks safe canonicality and re-fetches the receipt. Missing safe/RPC data stays
recoverable; contradictions and reorg enter review. Relayers/account abstraction
are not supported. RPC safe agreement does not itself prove Ethereum settlement.

`gormstore.Migrate` is explicit: invoice, payment event, fulfillment outbox, quote
preparation and payment binding tables. No production startup calls it. Unique
invoice, shop/order, chain/transaction/log and outbox keys enforce idempotency.

## Isolated demo

`go run ./cmd/tapecommerce-demo` accepts only loopback RPCs and listens only on
127.0.0.1. Required environment: `TAPE_DEMO_SECRET` (32+ characters),
`TAPE_DEMO_CHECKOUT`, `TAPE_DEMO_MERCHANT`, `TAPE_DEMO_TENANT_HASH`,
`TAPE_DEMO_RPC_PRIMARY`, `TAPE_DEMO_RPC_SECONDARY`. Optional:
`TAPE_DEMO_TMP_ROOT` and `TAPE_DEMO_PORT` (default 0). Stdout startup is only
`{"port":N}`. The secret is never printed. SIGTERM/interrupt shuts down HTTP,
closes SQLite and deletes only the newly-created private DB subdirectory.

The command seeds an original USDT 1.00 digital launch kit, 1000 encrypted synthetic
licenses, and dynamically creates synthetic buyers. It calls the real
OrderService.CreateOrder, including original parent/child records and card
reservation. All HTTP routes require the bearer secret and reject browser Origin.
The fixed shop is `tapecommerce-isolated-demo`. HTTP endpoints are catalog,
buyers/new, orders/create, quotes/prepare, quotes/persist, invoices/submit,
invoices/status and invoices/claim under `/v1`. Status actively checks and delivers;
no background payment worker is needed. The demo timeout queue is in-process and
drained on requests. SQL atomic lifetime limits allow at most 1000 buyer and
1000 order creation attempts; failed attempts count too. Only restarting the
owned ephemeral DB resets this budget.

Two local RPC forwards may point to the same Anvil chain for a reproducible demo.
That demonstrates the parser and end-to-end flow, not independent production
provider safety. Production rollout still needs explicit configuration, deployed
contract review, independent finality providers and operational reconciliation.

## Verification

```sh
go test -race -count=1 ./internal/bootstrap/tapecommerce ./internal/modules/tapecommerce/... ./internal/architecture
go test ./internal/modules/fulfillment/... ./internal/modules/payment/integrationtest/callback ./internal/modules/payment/integrationtest/selfhost ./internal/modules/order/integrationtest/application
go build ./cmd/tapecommerce-demo
```

Bridge tests start with original unpaid order creation and test real ledger,
reservation, stock, delivery, encrypted payload, claim, concurrent replay,
review-after-claim, buyer/payer/amount scope, canceled/refunded/paid/changed orders,
stockout rollback, generic callback rejection and HTTP/lifetime-budget boundaries.
Their chain verifier is explicitly synthetic. Separate local HTTP RPC fixtures
exercise EVM parsing and safety checks. Earlier fake-outbox tests remain useful
for lease mechanics but are not evidence of real payment/stock integration.
All test databases and HTTP fixtures are owned temporary resources and cleaned up.
