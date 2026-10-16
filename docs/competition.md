# Competition submission notes

Checked 2026-10-17 (Asia/Taipei). [Official event](https://web3.okx.com/xlayer/build-x-series); [official form](https://forms.gle/EoDNbxQ65dev1wFd9). Deadline currently 2026-12-07 23:59 UTC, subject to official updates. The form requires GitHub and video URLs; keep the video at or below 180 seconds. Personal registration details are intentionally excluded from this source tree.

## Pitch

TapeCommerce helps authorised digital-goods sellers connect existing inventory to a verifiable TapeOut identity on X Layer. Sellers keep USDT prices; short-lived OKB quotes use the public OKX spot ticker with exact integer arithmetic. The checkout is designed for direct merchant payment, while order state and verified events gate stock delivery. The current showcase combines real read-only identity and market data with a clearly labelled local-chain payment-to-stock flow. Mainnet checkout and our own DeWEB publication are not live.

## 用户、收入与持续使用假设

首批对象是具有合法分发权的软件作者与数字授权商家。通过 GitHub 可复现案例、独立开发者教程与 TapeOut 社区展示招募试用；这些是计划，不是已发送的宣传或现有客户。首轮目标为5次访谈、3位完成核心任务的试用者，并记录至少1位愿意在真实上线后付费的反馈；均为待验证目标。

拟测试19 USDT/店/月的运营维护订阅、不加FX价差；链上开通和gas另行展示与授权。付费理由应是减少接入和对账工作、可靠交付、更新及运维，而非单纯“上链”。持续使用以30天留存、重复订单处理和支持工时衡量。目前没有已验证收入、GMV或续费数据。

## Difference and limits

We reuse the original commerce order/stock system rather than claiming it was built entirely during this event. New work connects TapeOut identity, auditable OKB quotes and payment evidence to delivery. Shopify's official documentation already describes cryptocurrency gateway integrations, and BTCPay supports self-hosted Bitcoin payment workflows; accepting crypto alone is not a unique claim. Our narrower experiment is TapeOut/X Layer identity and OKB pricing for a small digital-goods workflow.

Sources: [Shopify cryptocurrency documentation](https://help.shopify.com/en/manual/payments/additional-payment-methods/cryptocurrency), [BTCPay documentation](https://docs.btcpayserver.org/). We do not claim to be the first, only, safest or lowest-cost option.

## Video outline: 170 seconds

| Time | Show |
|---|---|
| 0–18s | Seller problem and the distinction between live read-only data and local payments |
| 18–40s | Actual TapeOut holder/container/live state, pinned block, third-party sample label |
| 40–62s | USDT product price, fresh OKX last/ts, exact OKB quote and expiry |
| 62–90s | Private Anvil payment using test funds; no viewer wallet |
| 90–116s | Verified event through Go order/stock to synthetic license and HTML download |
| 116–138s | Replay/idempotency and a genuine error state; never fabricate a pass |
| 138–158s | Target users, price hypothesis, trial plan and absence of revenue claims |
| 158–170s | Public source/product URLs and remaining mainnet/security/DeWEB milestones |

Record only accepted working behaviour. Retain the actual error screen if a step fails; do not edit local receipts into mainnet transaction evidence. The participant has authorized publication of the independent source and the final verified demo video. Competition form submission remains separate and has not been performed. The public release video is a 140-second demonstration; the table above is the longer recording outline.
