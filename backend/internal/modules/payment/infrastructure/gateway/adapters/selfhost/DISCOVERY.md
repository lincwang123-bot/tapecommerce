# Read-only candidate discovery

`NewDiscoverer` and the concrete verifier returned by `New` implement the separate `contract.ChainDiscoverer` port. It returns transaction hashes only. The application must call the existing `ChainVerifier.Verify` for every candidate before settlement and persist the cursor only after all candidates have been processed (transient verification failure retains the previous cursor).

Fixed discovery endpoints (no user-supplied URL, redirects disabled):

| Network | Discovery endpoint | Query |
| --- | --- | --- |
| BSC | https://bsc-rpc.publicnode.com | chain ID 56; finalized block; USDT recipient-filtered `eth_getLogs` |
| Ethereum | https://eth.drpc.org | chain ID 1; finalized block; USDT recipient-filtered `eth_getLogs` |
| Polygon | https://polygon.drpc.org | chain ID 137; finalized block; USDT recipient-filtered `eth_getLogs` |
| TRON | https://api.trongrid.io | solidity head; account TRC20 history with fixed USDT, recipient and confirmed filters |

The verifier's endpoints are unchanged. BNB documents that its default dataseed endpoints disable `eth_getLogs`; Ethereum PublicNode may reject finalized-range logs as archive requests requiring a personal token. These providers are therefore not used for those discovery paths. No API key is embedded or required by the current client; public endpoints remain subject to throttling, policy changes, and outages. A successful anonymous read is not a guarantee of free production capacity. Any future key support belongs in server configuration, never an invoice URL.

EVM cursors are the next decimal block number, starting strictly after the invoice creation barrier. Each page queries at most 100 finalized blocks. Invalid, removed, unfiltered, null, oversized, or 1000-or-more-log responses fail without a page/cursor. The finalized tag must be supported; there is no confirmation-count fallback. Provider honesty and silent server-side omission are external trust limits. Small chunks keep public RPC load bounded, but recovering a long outage takes many rounds, especially on fast-block networks.

TRON cursors encode a fixed inclusive window (at most 15 minutes), pagination fingerprint, last observed timestamp, page count and a fixed upper time bound shared by every window of the entire replay round. A request handles one page of at most 200 records. Every fingerprint request preserves the original filter and timestamp parameters. `links.next` is never fetched; it is only a continuation signal. Missing/repeated fingerprints, invalid ordering, inconsistent metadata, and a full page with no continuation fail closed. A stuck or invalid cursor requires retry/operational inspection; it is never skipped automatically. A completed window advances by one millisecond. After catching up to the solid head minus a one-minute indexing grace interval, the cursor resets to empty so future rounds replay invoice history and can recover late index arrivals. `Done` means caught up for this round, not permanently complete. This requires an application-level discovery retention period to bound historic replay work.

Network operations have bounded per-request and whole-operation timeouts and bounded response bodies. There are no signing, transfer, purchasing, or fulfillment calls.

Official references:
- https://ethereum.org/developers/docs/apis/json-rpc/#eth_getlogs
- https://developers.tron.network/reference/get-trc20-transaction-info-by-account-address
- https://docs.bnbchain.org/bnb-smart-chain/developers/json_rpc/json-rpc-endpoint/
- https://drpc.org/docs/ethereum-api

Run from `backend`: `go test ./internal/modules/payment/infrastructure/gateway/evmchain ./internal/modules/payment/infrastructure/gateway/tronchain ./internal/modules/payment/infrastructure/gateway/adapters/selfhost`.

## Bounded interactive polling (2026-09)

Each network processes at most two invoices sequentially, then waits two seconds.
Half of the batch is reserved for unexpired invoices; the rest selects the oldest
remaining due invoice. This keeps retained expired invoices progressing under
sustained new arrivals. Caught-up unexpired invoices become due after five seconds
throughout the payment window; expired invoices after five minutes. Catch-up pages
remain due after one second. Durable per-invoice leases and unchanged candidate
verification still govern every settlement. A discovery availability failure stops
the batch, and any failed scan backs off that network worker for thirty seconds.
There are no extra concurrent RPC workers or unbounded retries.

These are scheduling intervals, not an end-to-end SLA. Queue depth, page backlog,
provider latency/rate limits and chain finality add latency. TRON additionally has
its existing one-minute indexing grace period. Multi-process deployments retain
per-invoice leases but must budget aggregate RPC traffic across replicas.

`SELFHOST_RPC_FAILOVER_ENABLED=true` opts into one fixed EVM backup per operation.
BSC verification/head use PublicNode; BSC discovery uses `https://bsc.drpc.org`;
Polygon uses `https://polygon-bor-rpc.publicnode.com`. The backup restarts the whole
operation and rechecks chain ID, finality and transfer evidence, with no mixed
provider proof. Only RPC availability/response/finality-unavailable failures retry;
pending, mismatched evidence, wrong chain and invalid input fail without retry.
The existing total 25-second verify/discovery and 15-second head budgets are split
between attempts. With the flag unset, behavior remains single-provider. Neither
TRON nor Ethereum has a new backup. TRON indexed discovery requires a compatible
trusted API; no substitute was assumed. Public providers offer no guaranteed SLA.

Registry sources: https://docs.bnbchain.org/bnb-smart-chain/developers/json_rpc/json-rpc-endpoint/
and https://docs.polygon.technology/pos/reference/rpc-endpoints
(PublicNode's Polygon page identifies the `polygon-bor-rpc` API endpoint).
