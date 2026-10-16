# Fixed-network USDT receipt verification

The production adapter is adapters/selfhost.New(), implementing paymentcontract.ChainVerifier without application imports of infrastructure. Network(channelType) maps only usdtbep20→bsc, usdttrc20→tron, usdtpolygon→polygon and usdterc20→ethereum. No unrecognized value falls back to another chain. Caller input cannot override RPC, token contract or decimals.

| Network | Chain ID | Fixed token contract | Decimals | Fixed read-only RPC |
|---|---:|---|---:|---|
| BSC Binance-Peg USDT | 56 | 0x55d398326f99059ff775485246999027b3197955 | 18 | https://bsc-dataseed.bnbchain.org |
| Ethereum USDT | 1 | 0xdac17f958d2ee523a2206206994597c13d831ec7 | 6 | https://ethereum-rpc.publicnode.com |
| Polygon USDT (USDT0) | 137 | 0xc2132d05d31c914a87c6611c10748aeb04b58e8f | 6 | https://polygon.drpc.org |
| TRON USDT | non-EVM (internal ID 0) | TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t | 6 | https://api.trongrid.io |

Polygon's previous USDT token has been upgraded in place to USDT0 at the same contract address. It must not be presented as an unrelated new token or blindly substituted with another bridged address.

## Evidence and business boundary

ReadHead checks the fixed chain and returns latest Number/Hash/Timestamp solely as an invoice creation barrier. Verify requires nonzero MinBlockNumber, receipt block strictly later than that barrier, EarliestPaidAt, exact Recipient and integer AmountUnits. Amounts never use floats. A six-digit UI amount uses a 10^12-unit step on BSC and one-unit step on the other three networks.

EVM receipt status must be 0x1. Its transaction/block identifiers must agree with every considered USDT Transfer log and the canonical receipt-height block. The latest finalized block must cover that block. No latest/confirmation-count fallback is permitted. Exactly one recipient-and-amount event must match; no transfer aggregation or arbitrary first-match selection. Removed logs, duplicate log indexes and malformed address padding are rejected. TRON uses walletsolidity receipts and blocks, SUCCESS, actual inclusion in the canonical solidified block, matching millisecond timestamps, fixed contract, Base58Check addresses, and identical exact-event constraints. TRON LogIndex is the zero-based index in the full receipt log array.

Result is evidence only: normalized txid, log_index, integer amount_units, paid_at, block identity, sender/recipient and finalized height. It proves neither identity nor order ownership. Caller must freeze the invoice first and enforce database uniqueness on chain+txid+log_index with atomic settlement.

Error kinds are invalid_input, pending, mismatch, unavailable, with fixed reason codes. Missing/unconfirmed receipts do not settle. A provider without finality support returns unavailable. Each attempt stays on one fixed endpoint, rejects redirects, ignores proxy environment settings, and never broadcasts a transaction. By default there is no retry; `SELFHOST_RPC_FAILOVER_ENABLED=true` enables one whole-operation attempt on a fixed BSC/Polygon backup after an availability failure, rechecking the complete proof within the original time budget (see ../adapters/selfhost/DISCOVERY.md). Verify is bounded to 25 seconds. RPC response size is bounded (EVM 2 MiB; TRON canonical block 4 MiB). Larger blocks safely return unavailable. This is RPC-based verification, not independent consensus validation; provider honesty remains a trust boundary.

## Primary sources

- [BNB Chain RPC registry and chain ID](https://docs.bnbchain.org/bnb-smart-chain/developers/json_rpc/json-rpc-endpoint/), [finality API](https://docs.bnbchain.org/bnb-smart-chain/developers/json_rpc/bsc-api-list/), [USDT contract mapping](https://explorer.bnbchain.org/asset/USDT-6D8).
- [Tether official Ethereum and TRON contracts](https://tether.to/en/supported-protocols/).
- [Ethereum JSON-RPC](https://ethereum.org/developers/docs/apis/json-rpc/), [ERC-20 Transfer ABI](https://eips.ethereum.org/EIPS/eip-20), [PublicNode operator endpoint](https://ethereum.publicnode.com/).
- [Polygon official RPC registry](https://docs.polygon.technology/pos/reference/rpc-endpoints), [finalized block semantics](https://docs.polygon.technology/pos/concepts/finality/finality), [official mapped contracts](https://docs.polygon.technology/pos/reference/mapped-tokens).
- [Polygon USDT0 upgrade announcement](https://polygon.technology/blog/native-usdt0-comes-to-polygon-for-lower-fees-and-deeper-liquidity), [USDT0 project's Polygon contract](https://usdt0.to/ecosystem/polygon).
- [TRON event address/data decoding](https://developers.tron.network/docs/event), [Base58/hex address format](https://developers.tron.network/docs/account), [solidified transaction receipts](https://tronprotocol.github.io/documentation-en/api/http/block-and-tx-query/gettransactioninfobyid/).

## 2026-09-10 read-only endpoint checks

All checks used only public contract metadata and block headers, no customer transaction or balance. Default Python proxy inheritance first caused HTTP errors on Ethereum/Polygon; explicit direct transport succeeded without changing machine proxy settings. Production Go transport likewise has no environment proxy.

- BSC: chain ID 0x38, finalized block available, decimals() returned 0x12 (18). A historical finalized eth_call lacked trie state; Verify does not need eth_call.
- Ethereum: Cloudflare's documented endpoint returned chain ID 1 but finalized returned RPC -32046 and decimals RPC -32603. The second and final tested provider, PublicNode, returned a finalized block and decimals() 6, so its fixed endpoint is selected. Ethereum has no runtime failover.
- Polygon: official-listed dRPC returned chain ID 137, a finalized block, and decimals() 6.
- TRON: TronGrid returned solidified current block and read-only triggerconstantcontract decimals() returned 6. Constant evaluation did not sign or broadcast any transaction.

These checks show endpoint/protocol availability at that moment, not proof of a real payment or permanent provider availability. Tests are in-memory transport fixtures; no DB or live funds are used.
