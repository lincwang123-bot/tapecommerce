# Licenses and attribution

TapeCommerce's independently authored Node.js showcase, browser UI, pricing/error modules, read-only adapter, tests and documentation are licensed under **GNU GPL version 3 only (GPL-3.0-only)**. The complete license text is at [LICENSE](LICENSE). This authorization does not change third-party or separately licensed component terms.

| Component | License and source |
|---|---|
| Dujiao-Next Go backend and TapeCommerce modifications | GPL version 3; original license at `backend/LICENSE` and upstream README retained. The complete `backend/internal` tree, isolated demo command, pinned module versions and build instructions provide the corresponding source used by this demo executable. |
| TapeCommerce Node/UI, pricing, errors and read-only modules | GPL-3.0-only, with SPDX headers in the source and root license text. |
| Six existing Coco9 Storefront UI themes, shared renderers, CSS and generated images | MIT, Copyright (c) 2026 Coco9 contributors. Original `web/storefront-ui/LICENSE`, image provenance `ASSETS.md` and source revision `UPSTREAM.md` are retained. New `commerce.js` follows its own GPL-3.0-only declaration. |
| TapeCommerceCheckout Solidity | Existing MIT SPDX declaration retained; OpenZeppelin Contracts 5.4.0 MIT license retained in `licenses/`. Solidity files retain their own SPDX identifiers. |
| Foundry Anvil 1.7.1 | MIT OR Apache-2.0; both upstream license texts retained in `licenses/`. |
| ethers and other dependencies | Versions fixed by package/module lockfiles; installed dependencies retain their own licenses and notices. |

The Solidity MIT declaration does not replace the GPL license for the Go backend or other original TapeCommerce code. Binary redistribution must preserve the applicable notices and provide the applicable corresponding source. This repository preserves the full Go library tree used by the demo, including the original backend README. This inventory does not offer a legal conclusion about every component boundary.

Excluded from this repository: original operational service-management scripts, production configuration and data, personal submission fields, private keys, the original attachment archive, unrelated storefront/admin UI, and private Git history. The fullstack UI build tag is outside this demo's build target. No official TapeOut, OKX or upstream endorsement is claimed.
