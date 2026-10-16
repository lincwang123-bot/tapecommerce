# 私有 OKB 报价签名服务

此服务为正式 Go 商城接线准备，**默认关闭，未配置生产私钥、未部署或广播主网交易**。它不启动 Anvil，不创建商家钱包，不使用第三方 TapeOut 样例冒充商家。六个原商城模板可以共用同一套受鉴权的 Go 订单/付款接口；浏览器不直接访问此签名服务。

## 调用协议与信任边界

两个端点均要求 `Authorization: Bearer <server-secret>`，拒绝任何 `Origin` header，没有 CORS 或重定向。Node 仅监听 `127.0.0.1` 或 `::1`。不同机器间的调用需要独立部署受信 TLS/mTLS 通道，不能把此端口映射为公网浏览器 API。

```text
GET  /internal/tapecommerce/health
POST /internal/tapecommerce/quote
```

`health` 每次实际核验前置条件，成功返回：

```json
{"ready":true,"mode":"live","chainId":196,"tenant":"shop_example","checkout":"<configured contract>","merchant":"<configured merchant>","quoteSigner":"<configured signer>"}
```

Go 必须比对这里的固定字段与自身配置；监听成功或 HTTP 进程存活不代表可以付款。

`quote` 只接受以下八个字段：

```json
{"orderId":123,"tenant":"shop_example","buyer":"user:123","currency":"USDT","amountUSDT":"1.00","status":"pending","payer":"<buyer wallet>","expiresAtMs":1799999999000}
```

`orderId` 是 JavaScript 安全正整数；`buyer` 为 `user:<uid>` 或 `guest:<orderId>`，访客 scope 必须匹配该订单。上述只是协议形状示例，不是可直接使用的真实订单。`tenant/buyer/amountUSDT/status/expiresAtMs` 必须由 Go 在验证会话/访客能力、读取原订单后确定；不得透传浏览器值。Node 不拥有原订单数据库，因此信任持有专用 Bearer 的 Go 端口在此边界完成订单授权。`payer` 要由订单准备流程固定。

成功返回现有 `signTrustedOrder` 的 `{domain,types,invoice,signature,fxSnapshot}`。Go 再次锁定原订单，核对金额/买家/有效期/配置，并将 invoice、FX 和 signature 与原 payment 原子持久化，完成后才向买家返回付款数据。调用者不能覆盖 merchant、contract、signer 或价格来源。错误统一为 `{"error":"code"}`；验证失败为4xx，身份/RPC/签名不可用、超时、未启用、过载为503。响应不包含上游报错、secret路径或密钥。

浏览器通过自己的钱包调用固定合约 `payOKB(invoice,signature)`，`msg.value` 使用服务端签名的整数 wei；返回的 txHash 只是提交线索。只有 Go 独立校验 receipt/event、双RPC safe状态并原子入账/库存交付后，前端才能显示已交付。

## 启用前置与配置

需要先在原 Studio 的商家鉴权会话中完成原 `wallet-challenge` / `wallet-verify` nonce 消息签名绑定。Node 只读已有 `tape_wallet_bindings`，验证当前 tenant 的 wallet、revision、verified_at、revoked_at；不会创建表或伪造绑定。报价取价后签名前、返回前再检查 revision，撤销立即阻止新报价。商家必须提供自己持有的真实 processorNumber/tokenId；当前商家身份缺失时保持503。

私有环境配置（**不得放进公开前端配置或源码仓库**）：

```sh
TAPE_QUOTE_ENABLED=false
TAPE_QUOTE_HOST=127.0.0.1
TAPE_QUOTE_PORT=18373
TAPE_QUOTE_TIMEOUT_MS=15000
TAPE_QUOTE_TENANT=<existing-Studio-shop-id>
TAPE_QUOTE_MERCHANT=<verified-merchant-public-address>
TAPE_QUOTE_PROCESSOR_NUMBER=<zero-based-processor-index>
TAPE_QUOTE_TOKEN_ID=<positive-token-id>
TAPE_QUOTE_CHECKOUT=<approved-deployed-contract>
TAPE_QUOTE_CHECKOUT_CODE_HASH=<verified-runtime-keccak256>
TAPE_QUOTE_SIGNER_ADDRESS=<dedicated-quote-signer-public-address>
TAPE_QUOTE_RPC_URL=https://rpc.xlayer.tech
TAPE_QUOTE_BINDING_DB=/absolute/path/to/existing-studio.db
TAPE_QUOTE_AUTH_SECRET_FILE=/absolute/private/path/to/go-node-auth-secret
TAPE_QUOTE_SIGNER_KEY_FILE=/absolute/private/path/to/dedicated-quote-signer-key
```

secret文件必须是真实普通文件，不能是symlink；权限仅owner可读写（例如0600），最多1024字节。auth secret为32–256字节无空白文本；signer key为专用签名账户的`0x`加64位hex，支持末尾换行。不能把用户助记词、主钱包私钥或私钥文本发到聊天；配置时由操作者在受控主机提供专用文件。服务不生成这些生产文件；缺失、权限过宽或地址不匹配就拒绝启动。程序不会读取raw-key环境变量。

```sh
npm --prefix platform ci --omit=dev --ignore-scripts
node platform/src/tapecommerce/quote-main.mjs
```

`TAPE_QUOTE_ENABLED` 只有精确值`true`才启用；否则入口退出1，且不读取密钥。依赖沿用platform已锁定ethers，不引入钱包SDK、Anvil或新的依赖。Go私有客户端应有请求超时、响应体上限且拒绝重定向；建议总超时略大于Node的15秒，且不把网络成功当作订单成功。

每次health和quote均通过真实adapter读取 `eth_getBlockByNumber("safe")`，固定该区块号读取Factory/processor/NFT/container/live，并复核区块hash。报价服务随后在**同一区块**读取checkout runtime、`CHAIN_ID()`、`quoteSigner()`、`eip712Domain()`并再次复核hash。任何`latest`替代、代理pin改变、holder不匹配、未open/未live、代码hash不符、错误domain或signer都拒绝。codehash必须来自对实际部署及源码/immutable参数的核验；不能把任意远端返回的hash自动信任为配置。

OKX数据沿用固定公开endpoint、`SPOT OKB-USDT last`、30秒新鲜度与最多5秒交易所时钟偏移。没有旧报价回退；服务进程内拒绝ticker时间倒退，不声称跨实例全局单调。USDT两位金额除以OKB价格，使用整数向上取整到wei、无价差，invoice有效60秒。Node限8KiB请求、128KiB上游RPC响应、2个并发工作、每分钟120次已鉴权请求；网络读有逐请求及总超时，整体取消会传至RPC/OKX。

## 主网部署准备仍单独处理

现有 `contracts/tapecommerce/scripts/prepare-deployment.mjs` 可编译固定artifact并生成未签名部署交易。它只接收三个公开地址；不要把私钥传给参数。owner可以使用经操作者批准的商家钱包，quote signer应使用独立签名账户，deployer是实际支付部署gas的钱包；这些角色不能因地址相同或项目配置存在而自动获得授权。

```sh
cd contracts/tapecommerce
npm run prepare:deployment -- --owner <approved-owner-public-address> --quote-signer <dedicated-quote-signer-public-address> --deployer <approved-deployer-public-address> --offline
```

这是**命令设计，未执行**。去掉`--offline`才会对指定部署地址做只读`eth_chainId/eth_estimateGas/eth_gasPrice`，仍不广播；估算不是费用上限。输出含`sent:false`和`deploymentAddress:null`。真实部署需要先核对源码、固定solc/optimizer/evmVersion/ABI/bytecode、构造参数与钱包确认的gas预算。仅在部署授权及实际成功receipt后，核对链196、合约代码、owner、quoteSigner、domain并写入服务配置；签名服务本身永远不部署合约。

## 上线边界

当前仍需正式商家nonce绑定及TapeOut身份、已部署checkout与runtime pin、专用signer、安全文件与Go生产接线；源码准备不等于主网已启用。主网Go收据验证需两个真正独立provider，两个不同URL不自动构成独立信任来源，safe也不等于L1结算证明。

一单一invoice、payer固定，过期签名不可覆盖。同nonce加速/替换交易、用户关闭页面丢失txHash、safe等待跨原订单超时，应通过受控对账/复核处理，不能允许任意替换txHash后直接交付。链上付款原子直达merchant，不会因为后续库存故障或订单取消自动退款。已签发的旧报价在TTL内可能继续有效；暂停新报价/撤销绑定不会在链上撤销历史签名，紧急signer轮换需独立授权。

钱包集成依据：[官方网络参数](https://web3.okx.com/onchainos/dev-docs/xlayer/developer/build-on-xlayer/network-information)、[切换网络](https://web3.okx.com/onchainos/dev-docs/wallet/dapp-connect/chains/evm/web-add-network)、[发送交易](https://web3.okx.com/onchainos/dev-docs/wallet/dapp-connect/chains/evm/web-send-transaction)、[provider事件](https://web3.okx.com/onchainos/dev-docs/wallet/dapp-connect/chains/evm/provider)。主动点击后请求账户/切换`0xc4`，4902才提示添加官方网络；账户或链变化时丢弃未提交的付款操作，必须重新核验。不要只靠交易对象里的chainId字段判断钱包当前网络。

验证命令：

```sh
node --test platform/test/tapecommerce-quote-server.test.mjs platform/test/tapecommerce-fx.test.mjs scripts/tapecommerce/readonly-adapter.test.mjs
```

本轮上述测试 79/79 通过，覆盖实际 EIP712 签名恢复、严格请求形状/金额、配置和文件权限、身份/合约/区块hash错误、绑定撤销、汇率时间倒退、并发限流恢复及超时向上游 fetch 的取消传播。

测试使用合成钱包、临时文件/SQLite与loopback HTTP fixture，不访问生产key或广播。HTTP服务器在测试cleanup中关闭，临时secret/DB目录删除；测试进程正常退出，没有留下测试服务。实际主网商家交易验证仍未进行。
