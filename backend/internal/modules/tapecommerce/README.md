# TapeCommerce payment and original-stock bridge

The original-order payment bridge is implemented and exercised on isolated SQLite
with original order creation, payment ledger, reserved card inventory, fulfillment
and authorized claim. The original storefront now has an explicit live composition,
disabled by default. It uses the original member/guest authorization and never
starts the demo runtime. A zero-value Service/Bridge is disabled, and DeliverOne
still requires an AtomicAdmission implementation.

## Order and payment boundary

`bootstrap/tapecommerce.Bridge` provides private server-to-server Prepare, Persist,
Submit, Status and ClaimDelivery ports. The original Go storefront authorizes the member or guest order and supplies an
immutable owner scope; browser-supplied prices, buyer IDs, shop IDs and claimed
payment results are never trusted. The isolated demo has a separate trusted
server-to-server adapter and is never used by the live routes. Configuration pins shop, checkout, tenant hash, merchant and payment channel.

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

The live storefront continues to use the original authorized order fulfillment
download, containing the original stock payload. The demo ClaimDelivery additionally
checks invoice/review/refund state. Submit/Status return no card payload.
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
preparation and payment binding tables. Live startup calls it only when both
`tapecommerce.enabled` and the explicit `tapecommerce.migrate` option are true. Unique
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

## Original storefront live integration

See `docs/tapecommerce/evidence/live-payment-integration.md` and
`docs/tapecommerce/quote-service.md` for the configuration and evidence. New routes
use the existing `/api/v1` envelope and original Bearer/Guest credentials:

- `GET /public/tapecommerce/config`
- `POST /orders/:order_no/tapecommerce/quote` accepts only `payer`.
- `POST /orders/:order_no/tapecommerce/submit` accepts only `invoice_id` and `transaction_hash`.
- `GET /orders/:order_no/tapecommerce/status` reads/reconciles the owned invoice.
- Guests use the same paths under `/guest` and the existing Guest authorization.

The service creates no users, funds no wallets and never sends blockchain
transactions. Wallets pay the exact signed invoice themselves. Existing USDT
payment flows are preserved; the generic CreatePayment path rejects TapeCommerce
before reserving balance or creating any payment row. Channel metadata can be
registered through the existing admin channel API using provider `tapecommerce`,
channel `okb`, interaction `wallet`, zero fees and empty config_json. All secret,
identity, contract and RPC configuration stays in operator configuration.

Live readiness requires the private signer's actual chain/identity/contract
preflight and matching tenant, checkout, merchant and signer. Anonymous config
requests share a five-second cache, including failure. New signing performs fresh
preflight inside the private Sign endpoint; repeating an existing quote performs
fresh health checking. Repeated active quotes preserve the invoice/signature;
canceled, already-paid, repriced and expired orders cannot retrieve a payable
quote. An expired invoice is never overwritten: create a new order instead.

Enabled runtimes register a bounded durable reconciliation worker with the app
Start/Stop lifecycle. Submitted hashes continue through safe verification and
atomic stock release after the page closes. Disabled mode performs no migration,
RPC or reconciliation; authorized status can still read an existing invoice and
otherwise returns 404 so ordinary USDT checkout remains usable.

Only pure online USDT, local automatic-stock orders are supported. Reseller,
affiliate, manual/upstream, wallet-mixed and fee-bearing orders fail closed.
Order cancellation/refund/stockout before admission enters review rather than
releasing stock. A lost hash never submitted to Go or a speed-up replacement
requires reconciliation outside this first supported path; no public recovery
endpoint can rewrite an invoice or claim payment success.
