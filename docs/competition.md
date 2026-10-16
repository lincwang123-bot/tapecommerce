# Competition submission notes

Checked 2026-10-17 (Asia/Taipei). [Official event](https://web3.okx.com/xlayer/build-x-series); [official form](https://forms.gle/EoDNbxQ65dev1wFd9). Deadline currently 2026-12-07 23:59 UTC, subject to official updates. The form requires GitHub and video URLs; keep the video at or below 180 seconds. Personal registration details are intentionally excluded from this source tree.

## Public links

- [Live showcase](https://tape.coco9.cc): HTTPS and the isolated payment/delivery flow were accepted on 2026-10-17, including desktop and mobile layouts. Mainnet payments remain unlaunched.
- [Source repository](https://github.com/lincwang123-bot/tapecommerce): independent GPLv3 source, with original backend and Solidity licenses preserved.
- [140-second video release](https://github.com/lincwang123-bot/tapecommerce/releases/tag/v0.1.0-demo) and [direct MP4](https://github.com/lincwang123-bot/tapecommerce/releases/download/v0.1.0-demo/demo.mp4).

## Pitch

TapeCommerce helps authorised digital-goods sellers connect existing inventory to a verifiable TapeOut identity on X Layer. Sellers keep USDT prices; short-lived OKB quotes use the public OKX spot ticker with exact integer arithmetic. The checkout is designed for direct merchant payment, while order state and verified events gate stock delivery. The current showcase combines real read-only identity and market data with a clearly labelled local-chain payment-to-stock flow. Mainnet checkout and our own DeWEB publication are not live.

## 用户、收入与持续使用假设

首批对象是具有合法分发权的软件作者与数字授权商家。通过 GitHub 可复现案例、独立开发者教程与 TapeOut 社区展示招募试用；这些是计划，不是已发送的宣传或现有客户。首轮目标为5次访谈、3位完成核心任务的试用者，并记录至少1位愿意在真实上线后付费的反馈；均为待验证目标。

拟测试19 USDT/店/月的运营维护订阅、不加FX价差；链上开通和gas另行展示与授权。付费理由应是减少接入和对账工作、可靠交付、更新及运维，而非单纯“上链”。持续使用以30天留存、重复订单处理和支持工时衡量。目前没有已验证收入、GMV或续费数据。

## Difference and limits

We reuse the original commerce order/stock system rather than claiming it was built entirely during this event. New work connects TapeOut identity, auditable OKB quotes and payment evidence to delivery. Shopify's official documentation already describes cryptocurrency gateway integrations, and BTCPay supports self-hosted Bitcoin payment workflows; accepting crypto alone is not a unique claim. Our narrower experiment is TapeOut/X Layer identity and OKB pricing for a small digital-goods workflow.

Sources: [Shopify cryptocurrency documentation](https://help.shopify.com/en/manual/payments/additional-payment-methods/cryptocurrency), [BTCPay documentation](https://docs.btcpayserver.org/). We do not claim to be the first, only, safest or lowest-cost option.

## Verified demo: 140 seconds

Watch the [v0.1.0-demo release video](https://github.com/lincwang123-bot/tapecommerce/releases/tag/v0.1.0-demo). The final video is 140 seconds (2:20), 1920×1080, H.264/AAC. It uses one continuous browser recording with framing, subtitles and a standard synthetic voice; no payment result is replaced or presented as mainnet execution.

| Approximate time | Actual recorded action |
|---|---|
| 0–25s | Explain the seller problem and show the real read-only TapeOut holder/container/live/block result |
| 39–49s | Select the product, create a pending order, receive the OKX quote and expand exact OKB amount, source, last and timestamp |
| 63–67s | Pay with local Anvil test funds, receive the original stock delivery result and expand the explicitly local transaction hash |
| 77s | Download the authorized synthetic store kit |
| 89s | Download the same order again; the two files have matching SHA-256 hashes |
| 101–117s | Show the merchant view and availability boundaries |
| 117–140s | Explain the target-user/pricing hypotheses and unlaunched mainnet service |

Repeated download shows consistent file delivery; it does not by itself prove stock-transaction idempotency. The Go integration tests provide separate transaction/idempotency evidence. No failed-price or transaction-replay scene is claimed in this video. No real money, customer data or participant contact details are shown.

## Work added on 2026-10-16–17

The public source includes the newly developed TapeOut safe-block identity adapter, fresh OKX OKB-USDT conversion and exact integer quotes, the checkout contract, Go payment-evidence/admission/stock bridge, and the isolated Anvil showcase with its UI and tests. These connect to the existing Dujiao-Next commerce foundation; they are not a claim that the entire backend was created during the event.

Review the actual files, tests and hashes in this independent [public repository](https://github.com/lincwang123-bot/tapecommerce) and `SOURCE_MANIFEST.json`. The participant has authorized the independent source and verified demo publication. Competition form submission remains separate and has not been performed.
