# Original-storefront Go payment integration

Date: 2026-10-17. Source implementation and isolated verification only. No production
service, production database, private operator environment, or mainnet transaction
was changed by this implementation.

## Implemented boundary

The original `/api/v1` storefront receives the default-disabled TapeCommerce
config/quote/submit/status routes. Member ownership comes from the existing
UserJWT middleware and tenant-scoped OrderService query. Guest ownership comes
from the existing Guest email/password verification, scoped to the exact original
order. Browser requests cannot supply buyer IDs, shop, merchant, amounts, FX,
signatures or raw invoices. The original fulfillment download remains the delivery
path. No live route calls the demo buyer, faucet, synthetic identity or demo DB.

Go prepares the exact persisted `money.Amount.String()` value, pins payer and
owner, calls the private Node signer, and rechecks the order/amount/owner/channel
under transaction before persisting. The private Node Sign endpoint performs its
fresh identity/contract/FX checks. The response domain/type/merchant/payer/math and
fixed operator identities are checked again before returning it to the browser.

Payment/order/children, safe receipt, original card allocation, fulfillment and
outbox completion commit in the original shared transaction. The worker scans
persisted submitted invoices and resumes after browser close or process restart;
new quotes are never required to confirm an already submitted payment. Each
reconciliation attempt is bounded and the worker shares the app shutdown lifecycle.

## Default-off operator configuration

All fields are under `tapecommerce` in server config; Viper also binds
`TAPECOMMERCE_ENABLED`, `TAPECOMMERCE_MIGRATE`, `TAPECOMMERCE_SHOP_ID`,
`TAPECOMMERCE_CHECKOUT`, `TAPECOMMERCE_MERCHANT`, `TAPECOMMERCE_TENANT_HASH`,
`TAPECOMMERCE_QUOTE_SIGNER`, `TAPECOMMERCE_CHANNEL_ID`,
`TAPECOMMERCE_RPC_PRIMARY`, `TAPECOMMERCE_RPC_SECONDARY`,
`TAPECOMMERCE_SIGNER_URL`, `TAPECOMMERCE_SIGNER_SECRET_FILE`.

Disabled is the default. It starts no TapeCommerce worker/RPC/migration. Migration
requires both enabled and explicit migrate=true. An enabled runtime without the
new schema or complete configuration stays unavailable; old payment channels
continue to work. No activation values are checked into source.

The signer URL permits loopback HTTP or operator-configured HTTPS, rejects URL
credentials/query/fragment and redirects, and reads only a regular private secret
file (no group/world permissions, 32+ characters). Client timeout is 20 seconds,
above the private signer's 15-second request budget. RPC URLs require two distinct
configured HTTPS endpoints. These configuration constraints do not themselves
prove provider independence.

The existing admin payment-channel API accepts `provider_type=tapecommerce`,
`channel_type=okb`, `interaction_mode=wallet`, zero fee and empty config_json. Its
ID must match the operator config. Generic CreatePayment explicitly rejects this
provider before wallet reservation or generic Payment creation; only the typed
invoice bridge can create the authoritative pending Payment.

`GET /public/tapecommerce/config` returns mode=live and ready/available only after
actual Node preflight and metadata match. Anonymous requests share a five-second
singleflight cache (failure included). New quotes use the Sign endpoint's fresh
preflight. Existing-quote retrieval checks current order state under the admission
lock order, so canceled/paid/refunded/repriced orders cannot be offered a payable
signature again. Expired quotes return `tapecommerce_quote_expired_new_order` and
are never overwritten. Disabled authorized status returns old stored state with
`channel_disabled`, or 404 when no invoice exists; no RPC is made.

## Verified cases

The live HTTP test suite uses original OrderService creation and real isolated
SQLite payment, reservation, encrypted card, fulfillment and outbox repositories.
Both member and guest flows run from unpaid orders. Guest creation includes the
original split child order, verifies inherited credentials, and verifies the
original authorized guest read exposes the delivered payload after settlement.

Cases cover wrong member, wrong guest password, guest cross-order invoice,
browser price-field injection, trusted signer amount/payer/domain tampering,
wallet switching, repeated quote identity, canceled/paid/expired quote retrieval,
channel amount policy, durable worker without HTTP polling, payment replay without
stock duplication, disabled migration/readiness, disabled historical reads,
concurrent health-cache access, multiple Start/Stop, signer redirect rejection and
private secret-file permissions. Chain evidence and signer are controlled test
fakes; this is not evidence of live OKB collection. Separate verifier tests exercise
RPC receipt/safe parsing. Existing original callback/selfhost/stock/order tests are
also run below.

Earlier test failures caught and corrected: the architecture function inventory
needed the explicit shared channel-policy use case; an expired-quote fixture had
changed only deadline and violated the immutable FX binding (fixture corrected to
an internally consistent historical snapshot); a real policy bug used the display
amount filter instead of `validatePaymentAmountForChannel` (production code now
uses the original write validator). No tests were weakened to accept a bad payment.

## Current supported limits

Pure online USDT, local automatic-stock orders only. Manual/upstream, affiliate,
reseller, mixed-balance and fee-bearing orders return ineligible and retain their
ordinary payment choices. No approval or payment is sent on behalf of the buyer.
The operator must provide a deployed reviewed checkout, real verified merchant
identity and signer configuration before availability becomes true.

Already-issued signatures cannot be revoked off chain. Cancellation/refund or
stock failure before safe admission enters review, never automatic delivery.
A browser-lost hash that was never submitted, or a replaced speed-up hash, needs
separate operator reconciliation; this implementation does not discover arbitrary
wallet transactions or overwrite the original invoice/hash. Public status contains
no card payload. Mainnet operation remains unactivated.

## Commands and final results

All commands below ran from `backend/`; completed test commands returned exit 0.
The final focused repetition was required by the added generic-provider guard.

```sh
go test -race -count=1 ./internal/bootstrap/tapecommerce ./internal/modules/tapecommerce/... ./internal/architecture ./internal/app/httpserver/middleware ./internal/app/container ./internal/modules/payment/integrationtest/callback ./internal/modules/payment/integrationtest/selfhost ./internal/modules/fulfillment/... ./internal/modules/order/integrationtest/application
```

The first broad run passed all payment/domain/RPC/store/stock/order packages but
reported the architecture ownership list failure described above. That inventory
was updated and the following affected final run passed:

```sh
go test -race -count=1 ./internal/bootstrap/tapecommerce ./internal/modules/payment/application ./internal/architecture ./internal/app/httpserver ./internal/app/httpserver/middleware ./internal/app/container ./internal/app
```

Passing package durations: bootstrap/tapecommerce 16.908s, payment/application
8.285s, architecture 4.461s, httpserver 4.945s, middleware 3.497s, container 3.969s,
app 4.625s. The broad run's unchanged original fulfillment suite passed in 4.663s
and original order integration suite passed in 8.002s; verifier/domain/store suites
also passed with race detection.

After adding the early generic-provider rejection, this final command passed:

```sh
go test -race -count=1 ./internal/bootstrap/tapecommerce ./internal/modules/payment/application ./internal/modules/payment/integrationtest/callback ./internal/modules/payment/integrationtest/selfhost ./internal/app/httpserver/middleware
```

Passing durations: bootstrap/tapecommerce 16.813s, payment/application 8.945s,
callback 2.117s, selfhost 29.948s, middleware 3.542s. The original generic
CreatePayment test verifies both use_balance false/true reject TapeCommerce
before any payment row or wallet/order mutation.

Builds (both exit 0):

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o ../.local/tapecommerce-build/tapecommerce-demo-v2 ./cmd/tapecommerce-demo
go build -trimpath -o /tmp/tapecommerce-server-live-check ./cmd/server
```

Linux demo binary SHA256: `023bfac0b19b6a2c083d234d34f6e77cea64292656851804832690214e22342d`; 38383159 bytes.
`go version -m` confirms trimpath=true, CGO_ENABLED=0, GOOS=linux, GOARCH=amd64.
The artifact contains no `/Users/linvwang` build path.

All test HTTP fixtures, worker contexts and temporary databases were closed by
cleanup; the owned temporary server build and editing helper were removed. No
application server, browser, production process or mainnet write was started. The
Linux demo artifact is retained at the requested ignored build path for the root
agent to package. Source was not committed by this subtask.
