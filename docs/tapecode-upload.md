# TapeCode 静态前端发布

本包保留现有 TapeCommerce 六套模板和公开演示交互。前端文件可放到 TapeCode / DeWEB；订单、报价与交付仍由 `https://tape.coco9.cc` 的独立沙盒服务处理。导入编辑器不等于已上链，发布静态文件也不会启用主网收款。

## 构建

在仓库根目录运行：

```sh
mkdir -p .local
node deploy/tapecommerce/tapecode-package.mjs --out .local/TapeCommerce-DeWEB
```

构建器拒绝覆盖已有输出。上传目录内只包含白名单 HTML、JavaScript、CSS、图片和许可证；ZIP、SHA256 清单及导入说明放在目录外。不要上传仓库、Linux 运行包、数据库或 `.local` 整个目录。

TapeCode 中使用“创建 → 导入文件夹”，选择 `TapeCommerce-DeWEB` 目录。它应成为独立项目，不覆盖浏览器中已有的其他项目。发布时再选择经链上核验、属于发布钱包的目标容器；不要把钱包地址直接当作容器地址。

## 后端和隔离环境

标准静态网关可通过 `TAPE_SHOWCASE_CORS_ORIGINS` JSON 数组配置精确 HTTPS Origin；默认是空数组。不要添加通配符或从请求参数自动接受新来源。

TapeCode 的隔离渲染器使用不透明 Origin，且不能依赖浏览器持久存储。仅对无真钱、无生产用户数据的独立 showcase，可设置 `TAPE_SHOWCASE_ALLOW_OPAQUE_DEMO_ORIGIN=1`。默认关闭；这个开关会允许任意 opaque 页面访问固定的公共演示端点，来源本身不证明身份，订单仍以高熵 `X-Demo-Token` 校验。它拒绝 Cookie、Authorization、正式 API 和 RPC。不能把这个模式移植到真实商城账户或收款后端。

测试须覆盖实际隔离渲染器，而不只检查普通静态 HTTP 页面。宿主可能禁止文件下载；交付 API 成功不等于浏览器已保存文件，页面必须如实说明并提供可查看的交付内容。

## 本地浏览器 QA 复现

以下命令均在仓库根目录执行。已验证环境为 Node `22.23.1`、Playwright `1.62.1` 和本机已安装的 Google Chrome；项目要求 Node `22.13+`，Go 版本须满足 `backend/go.mod` 的 `1.26.9`。脚本固定使用 `channel: 'chrome'`，只有 Playwright 自带 Chromium 不满足该配置。需要系统提供 `zip`、`unzip` 和 POSIX `ps`，且平台受锁定的 Anvil 原生包支持；未验证 Windows 原生运行。

先准备锁定依赖和本地 Go bridge。已有相同版本依赖及二进制时无需重新安装或构建；无需安装合约开发用的 Ganache，也无需启动正式演示服务。

```sh
npm --prefix platform ci --omit=dev --ignore-scripts
npm --prefix showcase ci --omit=dev --ignore-scripts
mkdir -p showcase/bin .local
(cd backend && CGO_ENABLED=0 go build -trimpath -o ../showcase/bin/tapecommerce-demo ./cmd/tapecommerce-demo)
node --test deploy/tapecommerce/tapecode-package.test.mjs
```

两份脚本通过 `--playwright-module` 使用已有的 Playwright 模块目录。将下方占位路径替换为本机实际的 `node_modules/playwright` 路径；该路径只是测试工具依赖，不会进入上传包。不需要为此升级项目依赖。

```sh
PLAYWRIGHT_MODULE=/absolute/path/to/node_modules/playwright
node -p "require(process.argv[1] + '/package.json').version" "$PLAYWRIGHT_MODULE"
```

先按上面的构建命令生成 `.local/TapeCommerce-DeWEB`。已有冻结包时直接使用，构建器不会覆盖它。保留相邻的 `.local/TapeCommerce-DeWEB.SHA256_MANIFEST.json`：隔离框架测试会读取此清单加载全部文件。每次测试的 `--report-dir` 必须尚不存在，父目录须存在；复跑时使用新的报告目录名称。

普通静态站根及网关子路径测试：

```sh
node deploy/tapecommerce/tapecode-browser-smoke.mjs \
  --bundle .local/TapeCommerce-DeWEB \
  --report-dir .local/tapecode-static-browser-qa \
  --playwright-module "$PLAYWRIGHT_MODULE"
```

此脚本覆盖 `/` 与 `/gateway/site/`，分别在 `1440×1100` 和 `390×844` 下检查六主题，共 24 个版面；四条订单流程验证创建、报价、演练付款、授权交付、实际 HTML 文件保存及刷新恢复。

隔离框架测试需单独下载官方渲染器作为测试输入。它不包含在公开源码或上传包中，也不会被脚本修改：

```sh
curl -fsSL --max-time 20 https://tapecode.ai/frame.html \
  -o .local/tapecode-frame-reference.html
node --input-type=module -e "import {readFileSync} from 'node:fs'; import {createHash} from 'node:crypto'; console.log(createHash('sha256').update(readFileSync(process.argv[1])).digest('hex'));" \
  .local/tapecode-frame-reference.html
node deploy/tapecommerce/tapecode-frame-smoke.mjs \
  --bundle .local/TapeCommerce-DeWEB \
  --report-dir .local/tapecode-frame-browser-qa \
  --frame-reference .local/tapecode-frame-reference.html \
  --playwright-module "$PLAYWRIGHT_MODULE"
```

本轮已通过测试的官方 `frame.html` SHA256 为 `af8a1387643303d4fd6286a62634c01b7121b21f065abcb21b73b4848272bbde`，取自实际浏览器报告的 `frameSHA256`。官方文件可能更新；新下载若哈希不同，应保留该哈希并重新验收，不能宣称复现了相同渲染器版本。

隔离测试在本地 `sandbox="allow-scripts"` iframe 中运行原始官方渲染器，覆盖两个尺寸的六主题、导航、内存订单凭证、报价、演练付款和授权交付。它断言没有发起下载，交付文件完整内容和许可可查看；刷新预览会丢失内存中的订单。普通站仍验证真实下载。

两份 QA 都拦截前端对固定 API 的请求，转交本地一次性 Node → Go → Anvil 服务；汇率和 TapeOut 身份使用明确的测试 fixture。付款执行真实的本地合约和 Go 库存交付，但不是主网付款，也不证明实时 OKX 行情、线上 TapeCode 导入、公网 CORS 或最终网关可用。测试无需更改生产 CORS 配置。

报告目录包含截图和 `report.json`。检查 `passed: true`、`errors: []`、预期版面与流程数量，并确认 `cleanup` 中每个自建进程的 `exited` 均为 `true`；脚本在正常完成和捕获失败时关闭自建浏览器、本地 HTTP 服务、Anvil 与 Go bridge，并按记录的 PID 核验退出。

## 发布验收

1. 核验目标 NFT 所在链、当前持有人、容器地址与有效状态；BNB Chain 身份不能填写为 X Layer 身份。
2. 逐文件核对上传包 SHA256，核对模板与第三方许可证。
3. 在编辑器检查六模板、桌面与手机布局、导航和沙盒订单流程；记录受宿主隔离策略限制的功能。
4. 查看创建、上传及激活的实际交易和费用，钱包确认由本人完成；不得把过往费用快照当作新交易授权。
5. 上链后从公开网关读回文件，核对 hash，再验证实际 API、报价、演练交付及错误提示。只有这一阶段通过后才能称为已发布。

参赛报名仍保持暂停。此文档不证明已创建容器、发布上链、完成真钱付款或提交报名。
