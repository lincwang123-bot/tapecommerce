# TapeCommerce

**Verifiable store identity. Auditable pricing. Digital delivery.**

TapeCommerce connects digital-goods inventory to TapeOut on X Layer. This repository is a standalone competition demonstration built on the Dujiao-Next Go commerce backend. It contains the corresponding Go library source used by the isolated demo, the new checkout contract, pricing and identity modules, and the showcase UI.

## What runs today

- **Live, read-only:** TapeOut identity on X Layer, pinned to a safe block with its hash rechecked. The default `1.2.2.tape` record is a third-party public sample, not a store owned by this project.
- **Live market data:** the fixed public OKX `OKB-USDT` spot ticker. A USDT price is divided by `last`, rounded up to OKB wei using integers and locked for a short invoice lifetime. This is a price reference, not an exchange trade or guaranteed execution price.
- **Isolated payment demo:** a private Anvil process executes the compiled checkout contract; the original Go order/stock modules verify payment evidence and deliver a synthetic license plus an HTML store kit. Two local read endpoints share the same simulator. They are not independent mainnet nodes.
- **Not launched:** mainnet checkout, real customer payments, our own DeWEB publication and commercial merchant service. There are no verified paying-user or revenue claims.

The demo does not connect the viewer's wallet or need real OKB. Anvil deliberately uses chain ID 196 to exercise the checkout's chain restriction; that does **not** turn it into X Layer mainnet. The UI labels payment receipts as local simulation.

Public address: **https://tape.coco9.cc** — deployment is in progress. The isolated VPS service has started; the Cloudflare hostname route is still being configured. Public availability has not yet been verified.

[Watch the 140-second demo video](https://github.com/lincwang123-bot/tapecommerce/releases/tag/v0.1.0-demo). The video labels live read-only data and local simulated payments throughout.

## Run locally

Requires Node **22.13+**, npm, Go **1.26+**, and a supported native Anvil platform. Use the committed lockfiles. No API key, production database, private wallet key, Redis or Docker is required. Outbound HTTPS to the official public OKX and X Layer endpoints must be available.

```sh
npm --prefix platform ci --omit=dev --ignore-scripts
npm --prefix showcase ci --omit=dev --ignore-scripts
mkdir -p showcase/bin
cd backend
CGO_ENABLED=0 go build -trimpath -o ../showcase/bin/tapecommerce-demo ./cmd/tapecommerce-demo
cd ..
HOST=127.0.0.1 PORT=18273 node showcase/server.mjs
```

Open `http://127.0.0.1:18273`. Stop with Ctrl-C. The supervisor closes its own Anvil and Go processes and removes its temporary database. No existing commerce configuration is loaded. On Linux amd64, a release binary can be built with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`.

The pinned Anvil package is installed without lifecycle scripts; the supervisor uses the platform package's native binary directly. If that package is unavailable for your platform, stop and inspect the fixed package/version; do not substitute an arbitrary executable. `ANVIL_BIN` and `TAPECOMMERCE_BRIDGE_BIN` are trusted operator overrides, never browser parameters.

Optional environment variables: `HOST`, `PORT`, `PUBLIC_ORIGIN`, `TAPECOMMERCE_DATA_DIR`, `ANVIL_BIN`, `TAPECOMMERCE_BRIDGE_BIN`. Public deployment must keep Node, Go and Anvil on loopback behind one HTTPS reverse proxy. The proxy must overwrite `X-Real-IP`; never expose raw RPC or bridge routes. The supervisor has limited order/session budgets, and synthetic state is disposable. Do not treat this as a production merchant payment service.

## Verify the source

```sh
node --test platform/test/tapecommerce-fx.test.mjs scripts/tapecommerce/readonly-adapter.test.mjs
npm --prefix showcase test
cd backend
go test -race ./internal/bootstrap/tapecommerce/... ./internal/modules/tapecommerce/... ./internal/architecture
cd ..
```

The Go tests use separate temporary databases and synthetic records. Node unit tests substitute deterministic public-data fixtures where needed; a unit-test price is not presented as a live market quote. Some integration tests require the locally built Go binary and the pinned Anvil binary; read their documented setup before running.

To reproduce the contract bytecode separately:

```sh
npm --prefix contracts/tapecommerce ci --ignore-scripts
npm --prefix contracts/tapecommerce run compile
```

Compare `contracts/tapecommerce/artifacts/TapeCommerceCheckout.json` ABI/bytecode with `showcase/assets/checkout.json`. The contract development test harness uses a pinned legacy Ganache dependency with known audit findings; it is separate from the Anvil showcase runtime and is not installed in the production service. The contract tests are not an independent security audit.

## Product and competition

The first proposed users are software creators and merchants with legitimate rights to distribute digital licenses. The proposed business is a **19 USDT per store per month** operations subscription, with no FX spread; this is a price hypothesis, not an available paid plan or evidence of willingness to pay. Initial interviews and trials will test whether identity, payment records and inventory integration reduce practical work.

We plan to reach initial testers through developer documentation and relevant independent-developer/TapeOut communities. No user counts, GMV, revenue or endorsements are claimed. TapeOut ownership is not a merchant quality guarantee. The server-based commerce workflow still requires an operator.

See [competition notes](docs/competition.md), [protocol boundaries](docs/protocol.md), and [license/source notes](LICENSING.md). Official event: [X Layer TapeOut Accelerate](https://web3.okx.com/xlayer/build-x-series). The official form requires a GitHub link and a demo video no longer than three minutes.

## Attribution and scope

The underlying product/order/stock implementation is based on [Dujiao-Next](https://github.com/dujiao-next/dujiao-next), with its GPLv3 license preserved at [backend/LICENSE](backend/LICENSE). Activity-period additions include TapeOut identity verification, OKX conversion snapshots, the checkout contract, verified-payment/stock integration and this isolated showcase. The previous commerce UI, operational deployment scripts, production configuration and user data are intentionally outside this standalone demonstration.

TapeOut, TapeKit, OKX, Foundry and OpenZeppelin are independent upstream projects. This project does not claim their endorsement or security review. The independently authored Node/UI, pricing/error modules, read-only adapter, tests and documentation use **GPL-3.0-only**; see [LICENSE](LICENSE) and [LICENSING.md](LICENSING.md). Solidity files retain their existing MIT SPDX identifiers. The file inventory and SHA-256 hashes are recorded in `SOURCE_MANIFEST.json`.
