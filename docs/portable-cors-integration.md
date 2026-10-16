# 静态前端 API 与精确 CORS 验证

本轮只修改本地源码与测试。**未部署 CORS 配置，未修改生产环境，未链上发布，也未证明 TapeCode 预览已能联网。** 正式付款仍为 `live.ready=false`；这里的交易均发生在自建、一次性 Anvil 沙盒，链 ID 196 不代表 X Layer 主网。

## 配置契约

- `showcase/public/ui.js`：`createApi()` 保留同源相对 `/api/`；静态构建可在输出文件中使用 `createApi({apiBase:'https://tape.coco9.cc'})`。只允许该精确 HTTPS 字符串或空字符串，不读 query、localStorage 或全局可变配置。所有请求使用 `credentials:'omit'`、`redirect:'error'`。
- `showcase/server.mjs`：`createShowcase({corsOrigins:[]})` 默认关闭跨域。CLI 的可选 `TAPE_SHOWCASE_CORS_ORIGINS` 是 JSON 数组，值必须是精确、规范的 HTTPS origin，最多 16 项，不接受 wildcard、`null`、路径、query、fragment、凭据或重复项。配置在启动沙盒前验证。
- 示例域 `https://123-1-0.gateway.example` 只用于本地测试，未写入生产配置。精确域名模式必须确认实际 Origin，不能使用整个公共网关或把 `null` 混入该白名单。opaque iframe 的公共演示兼容由下述独立开关控制。
- 只允许已有 `/api/config`、`/api/catalog`、`/api/identity` 和 demo 订单接口各自必要的 GET/POST。OPTIONS 验证实际路由、方法与 `Accept / Content-Type / X-Demo-Token`，返回精确 `Access-Control-Allow-Origin` 和 `Vary: Origin`。不返回 `Access-Control-Allow-Credentials`，跨域请求携带 Cookie/Authorization 时拒绝。
- `Sec-Fetch-Site: cross-site` 须匹配明确允许的 HTTPS Origin，或显式开启的 opaque demo 模式。未知 HTTPS 来源和缺失 Origin 不获得跨域权限。原订单 token、金额来源、重放与库存边界保持不变。
- 静态包需要完整模板资源和固定 `connect-src https://tape.coco9.cc` 策略；网关自身的 CSP、service worker、Origin 和外部网络能力还需实际验收。不能从本地 CORS 测试推出网关可访问性。

## 独立 opaque 公共演示模式

`sandbox="allow-scripts"` iframe 会使用 opaque origin，请求表现为字面字符串 `Origin: null`。为运行无真实资金的公开演示，`createShowcase({allowOpaqueDemoOrigin:true})` 可显式允许这些请求；默认值为 `false`。CLI 只有 `TAPE_SHOWCASE_ALLOW_OPAQUE_DEMO_ORIGIN=1` 才开启，其他值不会开启；本次没有修改任何生产环境值。

这不是对某个网站的身份验证：**任意 opaque 来源都可能发送 `Origin: null`，CORS 本身不构成身份认证。** 这里是匿名可试用的临时沙盒，订单访问始终需要原有独立 `X-Demo-Token` 能力票据。客户端不发送 cookie，服务端仍拒绝 Cookie/Authorization，不返回 `Access-Control-Allow-Credentials`；响应 `Access-Control-Allow-Origin: null` 仅限已有公开演示 `publicApiMethod` 路由和其必要方法。`/api/v1` 正式账户/订单、`/healthz`、RPC、静态资源及未知路径均不因这个开关对 opaque 请求开放，GET/OPTIONS 在这些路径拒绝且不返回允许来源头。

**该模式只适用于隔离、无真实资金的公共 demo，不适用于真实账户或真钱支付后端。** 没有修改正式 Go API、商家身份、私有签名服务或主网开关；`live.ready=false` 不变。现有限流、订单数量/生命周期限制、token 校验与防重放继续生效。网页部署链与沙盒的链 ID 也不能混同为主网收款证据。

## 精确命令与结果

在仓库根目录执行：

```sh
node --test showcase/test/server.test.mjs showcase/test/ui.test.mjs
node --check showcase/server.mjs
node --check showcase/public/ui.js
git diff --check -- showcase/server.mjs showcase/public/ui.js showcase/test/server.test.mjs showcase/test/ui.test.mjs
```

最终 12/12 测试通过，零失败；语法与差异检查 exit 0。覆盖默认关闭、非法配置、精确 origin、恶意后缀域、null 默认拒绝/单独开启、wildcard、方法和 header 拒绝、预检响应、无 cookie、同源兼容、跨域订单授权/报价/交付/重放、opaque 无权读取 formal/health 路由和正式收款保持关闭。测试使用受控 server fixture，不将此部分称为真实链交易。

随后重新构建当前 Go demo 并执行独立真实沙盒：

```sh
cd backend
go build -trimpath -o /tmp/tapecommerce-opaque-bridge-20261017 ./cmd/tapecommerce-demo
cd ..
TAPECOMMERCE_BRIDGE_BIN=/tmp/tapecommerce-opaque-bridge-20261017 node --test showcase/test/sandbox-cors.integration.mjs
```

构建 exit 0，最终集成测试 1/1 通过，测试耗时 1.428 秒。它自建 Anvil、部署真实合约、生成临时钱包、使用新临时 SQLite 和真实 Go 原订单/库存流程，分别完成同源、精确白名单跨域和显式 opaque demo 请求的创建订单→签发报价→实际 EVM 付款→safe 核验→授权交付→重放。错误及其他订单 token 为 404、提前交付为 409。报价汇率是受控测试快照 `117.91`，不是本轮实时 OKX 行情证据；请求传输是 loopback HTTP 并显式设置测试 Origin，不是实际浏览器 CORS/网关验收。

仅本地沙盒交易：

| 路径 | 状态 | 交易 hash | 重放 |
|---|---|---|---|
| 原同源 | delivered / safe | `0xe8f55ac11a87a1975c078131dd4dcb55e50fbf760023dba942c0645bc30945ed` | hash 不变 |
| 精确白名单跨域 | delivered / safe | `0xd7ad0d3c53132dd915d2f6a69945a20d26233763d401ac13f6c6a3832760468f` | hash 不变 |
| 显式 opaque demo | delivered / safe | `0xeccbd9deb695fe19022c7edae49c71b57adf61d32e953ba5e9dc49c754bad5cd` | hash 不变 |

## 清理与审查

每个 server fixture 在 cleanup 中断言 `server.listening === false`，并确认新连接失败。最终真实沙盒记录的子进程 PID 为 `95940`、`95945`；cleanup 使用 `process.kill(pid,0)` 确认均为 ESRCH，验证临时数据根目录为空后删除根目录，测试进程 exit 0。临时 Go 构建文件也已删除。前一版精确 CORS 测试的 PID `93546`、`93551` 同样已清理。未操作已有 localhost:18273 服务或其他生产服务。

独立只读审查未发现本轮 Origin、预检、API 目标或正式付款越界的阻断问题。该审查不替代尚未完成的实际 TapeCode 网关联网验收。
