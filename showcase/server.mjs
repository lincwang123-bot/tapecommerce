// SPDX-License-Identifier: GPL-3.0-only
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { randomBytes, createHash, timingSafeEqual } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { startSandbox } from './sandbox.mjs';
import { signTrustedOrder } from '../platform/src/tapecommerce/quote.mjs';
import { okxSpotRates } from '../platform/src/tapecommerce/okx-rate.mjs';
import { readIdentity } from '../scripts/tapecommerce/readonly-adapter.mjs';
import { storefrontRoutes } from './static-assets.mjs';

const TTL = 45 * 60_000;
const fail = (code, status = 400) => { throw Object.assign(new Error(code), { code, status }); };
const digest = token => createHash('sha256').update(token).digest();
const staticFiles = new Map([
  ['/', ['./public/index.html', 'text/html; charset=utf-8']],
  ['/index.html', ['./public/index.html', 'text/html; charset=utf-8']],
  ['/app.js', ['./public/app.js', 'text/javascript; charset=utf-8']],
  ['/styles.css', ['./public/styles.css', 'text/css; charset=utf-8']],
  ['/i18n.js', ['./public/i18n.js', 'text/javascript; charset=utf-8']],
  ['/ui.js', ['./public/ui.js', 'text/javascript; charset=utf-8']],
  ['/mark.svg', ['./public/mark.svg', 'image/svg+xml']],
  ...storefrontRoutes,
]);
const productMeta = { title: { zh: '商店启动工具包', en: 'Store launch kit' }, description: { zh: '可编辑的单页数字商品模板与发布清单。完成演示订单后下载。', en: 'An editable digital-product page and launch checklist, delivered after your demo order.' }, format: 'HTML', filename: 'tapecommerce-store-kit.html' };
const states = { quote_issued: 'quoted', submitted: 'submitted', detected: 'confirming', fulfillment_pending: 'confirming', delivered: 'delivered', review_required: 'review' };

function corsAllowlist(values) {
  if (!Array.isArray(values) || values.length > 16) throw new Error('invalid_cors_origins');
  for (const value of values) {
    let url;
    try { url = new URL(value); } catch { throw new Error('invalid_cors_origins'); }
    if (typeof value !== 'string' || url.protocol !== 'https:' || url.origin !== value || url.username || url.password
      || value.includes('*') || url.hostname.endsWith('.')) throw new Error('invalid_cors_origins');
  }
  if (new Set(values).size !== values.length) throw new Error('invalid_cors_origins');
  return new Set(values);
}

function publicApiMethod(path) {
  if (['/api/config', '/api/catalog', '/api/identity'].includes(path)) return 'GET';
  if (path === '/api/demo/orders') return 'POST';
  const match = /^\/api\/demo\/orders\/[a-f0-9]{32}(?:\/(quote|pay|delivery))?$/.exec(path);
  return match ? (['quote', 'pay'].includes(match[1]) ? 'POST' : 'GET') : null;
}

export function createShowcase({ sandbox, rates = okxSpotRates, lookup = readIdentity, publicOrigin, corsOrigins = [], allowOpaqueDemoOrigin = false, now = Date.now } = {}) {
  const allowedCorsOrigins = corsAllowlist(corsOrigins);
  if (typeof allowOpaqueDemoOrigin !== 'boolean') throw new Error('invalid_opaque_demo_origin_option');
  const orders = new Map(), limits = new Map(), identityCache = new Map();
  let healthy = true, identityActive = 0, activeCreates = 0, lifetimeCreates = 0;
  const product = async () => {
    const value = await sandbox.call('/v1/catalog');
    const first = value.products?.[0];
    if (!first || !Number.isSafeInteger(first.id) || !/^\d+\.\d{2}$/.test(first.amountUSDT)) fail('demo_catalog_unavailable', 503);
    return { ...productMeta, id: String(first.id), amountUSDT: first.amountUSDT };
  };
  const snapshot = order => ({ id: order.id, productId: order.productId, status: order.status, amountUSDT: order.amountUSDT,
    ...(order.quote ? { amountWei: order.quote.invoice.amountWei, quote: { amountWei: order.quote.invoice.amountWei, deadline: order.quote.invoice.deadline, fxSnapshot: order.quote.fxSnapshot } } : {}),
    ...(order.txHash ? { txHash: order.txHash, receipt: order.receipt } : {}), sourceMode: 'isolated_evm' });
  const authorized = (req, id) => {
    const order = orders.get(id), token = req.headers['x-demo-token'];
    if (!order || order.createdAt + TTL < now() || typeof token !== 'string' || token.length > 128 || !timingSafeEqual(digest(token), order.tokenHash)) fail('order_not_found', 404);
    return order;
  };
  const sync = async order => {
    if (!order.quote || !order.txHash || order.status === 'delivered') return;
    // The EVM receipt may exist while the first bridge notification was lost.
    // Retry its idempotent admission before polling; never pay a second time.
    const result = await sandbox.call(order.submittedToBridge ? '/v1/invoices/status' : '/v1/invoices/submit', {
      buyerId: order.buyerId, invoiceId: order.quote.invoice.invoiceId, ...(order.submittedToBridge ? {} : { txHash: order.txHash }),
    });
    order.submittedToBridge = true;
    order.status = states[result.state] || 'review';
    order.receipt = { txHash: order.txHash, blockNumber: result.blockNumber, safe: result.safe, sourceMode: 'isolated_evm' };
  };
  const locked = async (order, fn) => {
    if (order.busy) fail('order_busy', 409);
    order.busy = true;
    try { return await fn(); } finally { order.busy = false; }
  };
  const headers = { 'content-security-policy': "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'", 'x-content-type-options': 'nosniff', 'referrer-policy': 'no-referrer', 'permissions-policy': 'camera=(), microphone=(), geolocation=()', 'cross-origin-opener-policy': 'same-origin', 'cache-control': 'no-store' };
  const send = (res, status, body) => res.writeHead(status, { ...headers, 'content-type': 'application/json; charset=utf-8' }).end(JSON.stringify(body));
  const server = createServer(async (req, res) => {
    try {
      const url = new URL(req.url, 'http://localhost');
      const origin = req.headers.origin;
      const apiMethod = publicApiMethod(url.pathname);
      if (!['GET', 'POST', 'HEAD', 'OPTIONS'].includes(req.method)) fail('method_not_allowed', 405);
      if (req.method === 'OPTIONS' && allowedCorsOrigins.size === 0 && !allowOpaqueDemoOrigin) fail('method_not_allowed', 405);
      // A sandboxed DeWEB iframe has an opaque origin. This opt-in is only for
      // the public, disposable, test-fund demo: null is never caller identity.
      // Order authorization remains the independent per-order capability token.
      if (origin === 'null' && (!allowOpaqueDemoOrigin || !apiMethod)) fail('opaque_origin_forbidden', 403);
      if (req.method === 'GET' && url.pathname === '/healthz') return send(res, healthy ? 200 : 503, { status: healthy ? 'ok' : 'unavailable', sourceMode: 'isolated_evm' });
      if (req.method === 'GET' || req.method === 'HEAD') {
        const file = staticFiles.get(url.pathname);
        if (file) { const body = await readFile(new URL(file[0], import.meta.url)); res.writeHead(200, { ...headers, 'content-type': file[1] }).end(req.method === 'HEAD' ? undefined : body); return; }
      }
      if (!url.pathname.startsWith('/api/')) fail('not_found', 404);
      if (!healthy) fail('demo_unavailable', 503);
      const expectedOrigin = publicOrigin || `http://${server.address().address === '127.0.0.1' ? '127.0.0.1' : 'localhost'}:${server.address().port}`;
      const crossOriginAllowed = (typeof origin === 'string' && origin !== expectedOrigin && allowedCorsOrigins.has(origin))
        || (allowOpaqueDemoOrigin && origin === 'null' && apiMethod !== null);
      if (req.headers['sec-fetch-site'] === 'cross-site' && !crossOriginAllowed) fail('cross_site_request', 403);
      if (origin && origin !== expectedOrigin && !crossOriginAllowed) fail('invalid_origin', 403);
      if (req.method === 'OPTIONS') {
        if (!crossOriginAllowed) fail('method_not_allowed', 405);
        if (!apiMethod) fail('not_found', 404);
        if (req.headers['access-control-request-method'] !== apiMethod) fail('method_not_allowed', 405);
        const requestedHeaders = req.headers['access-control-request-headers'];
        if (requestedHeaders !== undefined && (typeof requestedHeaders !== 'string' || !requestedHeaders.trim()
          || requestedHeaders.split(',').some(value => !['accept', 'content-type', 'x-demo-token'].includes(value.trim().toLowerCase())))) fail('cors_headers_forbidden', 403);
      }
      if (crossOriginAllowed) {
        if (!apiMethod) fail('not_found', 404);
        if (req.method !== 'OPTIONS' && req.method !== apiMethod) fail('method_not_allowed', 405);
        if (req.headers.cookie || req.headers.authorization) fail('cors_credentials_forbidden', 403);
        res.setHeader('Access-Control-Allow-Origin', origin);
        res.setHeader('Vary', 'Origin');
      }
      // Reverse proxy must overwrite X-Real-IP. Direct deployment binds loopback.
      const ip = String(req.headers['x-real-ip'] || req.socket.remoteAddress).slice(0, 128);
      const time = now();
      for (const [key, entry] of limits) if (entry.until < time) limits.delete(key);
      for (const [key, order] of orders) if (order.createdAt + TTL < time && !order.busy) orders.delete(key);
      if (!limits.has(ip) && limits.size >= 4096) fail('demo_busy', 503);
      const rate = limits.get(ip) || { count: 0, creates: 0, identities: 0, until: time + 60000 };
      limits.set(ip, rate);
      if (++rate.count > 120) fail('rate_limited', 429);
      if (req.method === 'OPTIONS') {
        res.writeHead(204, { ...headers, 'access-control-allow-methods': apiMethod,
          'access-control-allow-headers': 'Accept, Content-Type, X-Demo-Token', 'access-control-max-age': '60' }).end();
        return;
      }
      let body = {};
      if (req.method === 'POST') {
        if (!/^application\/json(?:\s*;|$)/i.test(req.headers['content-type'] || '')) fail('json_required', 415);
        const chunks = []; let size = 0;
        for await (const chunk of req) { size += chunk.length; if (size > 4096) fail('request_too_large', 413); chunks.push(chunk); }
        try { body = JSON.parse(Buffer.concat(chunks).toString() || '{}'); } catch { fail('invalid_json'); }
        if (!body || Array.isArray(body) || typeof body !== 'object') fail('invalid_json');
      }
      if (req.method === 'GET' && url.pathname === '/api/config') return send(res, 200, {
        demo: { enabled: healthy && lifetimeCreates < 1000, label: 'Isolated EVM · test funds only', initialBalanceOKB: '100', sessionExpiresMinutes: 45 },
        live: { ready: false, reason: 'mainnet_checkout_not_activated', checkoutAddress: null, chainId: 196 },
        sampleIdentity: { processorNumber: '2', tokenId: '1', label: 'Third-party public TapeOut record' },
        merchantSteps: [{ id: 'template', status: 'ready' }, { id: 'products', status: 'ready' }, { id: 'identity', status: 'ready' }, { id: 'publish', status: 'partial' }],
        features: { identity: 'ready', publication: 'partial' },
        links: { rules: 'https://web3.okx.com/zh-hans/xlayer/build-x-series' }, sourceMode: 'isolated_evm',
      });
      if (req.method === 'GET' && url.pathname === '/api/catalog') return send(res, 200, { products: [await product()] });
      if (req.method === 'GET' && url.pathname === '/api/identity') {
        if (++rate.identities > 5 || identityActive >= 2) fail('rate_limited', 429);
        const processorNumber = url.searchParams.get('processorNumber'), tokenId = url.searchParams.get('tokenId');
        if (![processorNumber, tokenId].every(v => typeof v === 'string' && /^(0|[1-9][0-9]{0,76})$/.test(v))) fail('invalid_identity');
        const key = `${processorNumber}:${tokenId}`, cached = identityCache.get(key);
        if (cached && cached.until > time) return send(res, 200, cached.value);
        identityActive++;
        try {
          const identity = { ...await lookup({ processorNumber, tokenId, timeoutMs: 8000 }), checkedAt: now() };
          const value = { identity, provenance: 'xlayer_mainnet_readonly', sampleIsThirdParty: processorNumber === '2' && tokenId === '1' };
          if (identityCache.size >= 100) identityCache.clear();
          identityCache.set(key, { until: now() + 30000, value }); return send(res, 200, value);
        } catch { fail('identity_unavailable', 503); } finally { identityActive--; }
      }
      if (req.method === 'POST' && url.pathname === '/api/demo/orders') {
        if (++rate.creates > 4) fail('rate_limited', 429);
        if (orders.size + activeCreates >= 256 || activeCreates >= 8 || lifetimeCreates >= 1000) fail('demo_busy', 503);
        activeCreates++; lifetimeCreates++;
        try {
        const item = await product();
        if (body.productId !== item.id) fail('invalid_product');
        const wallet = await sandbox.payer();
        const { buyerId } = await sandbox.call('/v1/buyers/new', {});
        const result = await sandbox.call('/v1/orders/create', { buyerId, productId: Number(item.id), payer: wallet.address });
        const token = randomBytes(32).toString('hex'), id = randomBytes(16).toString('hex');
        const order = { id, dbOrderId: result.orderId, buyerId, productId: item.id, amountUSDT: result.amountUSDT, status: 'created', wallet, createdAt: now(), tokenHash: digest(token) };
        orders.set(id, order); return send(res, 201, { order: snapshot(order), token });
        } finally { activeCreates--; }
      }
      const match = /^\/api\/demo\/orders\/([a-f0-9]{32})(?:\/(quote|pay|delivery))?$/.exec(url.pathname);
      if (!match) fail('not_found', 404);
      const order = authorized(req, match[1]), action = match[2];
      if (req.method === 'GET' && !action) {
        if (!order.busy) await locked(order, () => sync(order));
        if (order.status === 'quoted' && Number(order.quote.invoice.deadline) * 1000 <= now()) order.status = 'expired';
        return send(res, 200, { order: snapshot(order) });
      }
      if (req.method === 'POST' && action === 'quote') return await locked(order, async () => {
        if (!['created', 'quoted', 'expired'].includes(order.status)) fail('order_not_payable', 409);
        if (order.quote && Number(order.quote.invoice.deadline) * 1000 > now()) return send(res, 200, { order: snapshot(order) });
        if (order.quote) fail('order_quote_expired_create_new', 409);
        const quote = await signTrustedOrder({ orderId: order.dbOrderId, buyer: order.buyerId, tenant: sandbox.tenant, checkoutContract: sandbox.checkout, signer: sandbox.signer, rates, now,
          orders: { async loadPayable() { const current = await sandbox.call('/v1/quotes/prepare', { buyerId: order.buyerId, orderId: order.dbOrderId, payer: order.wallet.address }); return { ...current, id: current.orderId, buyer: current.buyerId }; } },
          // This explicitly synthetic identity applies only to the private EVM.
          // Real TapeOut reads live in /api/identity and never authorize this demo.
          identities: { async verifyCurrent() { return { chainId: 196, status: 'verified', live: true, holder: sandbox.merchant, sourceMode: 'isolated_evm' }; } },
        });
        await sandbox.call('/v1/quotes/persist', { buyerId: order.buyerId, orderId: order.dbOrderId, invoice: quote.invoice, fxSnapshot: quote.fxSnapshot, signature: quote.signature });
        order.quote = quote; order.status = 'quoted'; return send(res, 200, { order: snapshot(order) });
      });
      if (req.method === 'POST' && action === 'pay') return await locked(order, async () => {
        if (order.txHash) { await sync(order); return send(res, 200, { order: snapshot(order) }); }
        if (order.status !== 'quoted' || !order.quote) fail('order_not_payable', 409);
        if (Number(order.quote.invoice.deadline) * 1000 <= now()) { order.status = 'expired'; fail('fx_quote_expired', 409); }
        const receipt = await sandbox.pay(order.wallet, order.quote);
        order.txHash = receipt.txHash; order.receipt = receipt; order.status = 'submitted';
        await sync(order);
        return send(res, 200, { order: snapshot(order) });
      });
      if (req.method === 'GET' && action === 'delivery') return await locked(order, async () => {
        if (order.status !== 'delivered') fail('delivery_not_ready', 409);
        const claim = await sandbox.call('/v1/invoices/claim', { buyerId: order.buyerId, invoiceId: order.quote.invoice.invoiceId });
        if (!claim.payload) fail('delivery_not_ready', 409);
        const content = await readFile(new URL('./assets/store-kit.html', import.meta.url));
        return send(res, 200, { filename: productMeta.filename, contentType: 'text/html', base64: content.toString('base64'), licenseCode: claim.payload });
      });
      fail('method_not_allowed', 405);
    } catch (error) {
      const status = Number.isInteger(error.status) && error.status >= 400 && error.status < 600 ? error.status : 503;
      const code = /^[a-z][a-z0-9_]{1,80}$/.test(error.code || '') ? error.code : 'service_unavailable';
      if (!res.headersSent) send(res, status, { error: code }); else res.end();
    }
  });
  server.requestTimeout = 20000; server.headersTimeout = 10000; server.keepAliveTimeout = 5000; server.maxRequestsPerSocket = 100;
  return { server, setUnhealthy() { healthy = false; }, async close() { healthy = false; server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); orders.clear(); await sandbox.close(); } };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  let app, sandbox, closing = false;
  const close = async (failed = false) => { if (failed) process.exitCode = 1; if (closing) return; closing = true; if (app) await app.close(); else await sandbox?.close(); };
  process.once('SIGTERM', () => close()); process.once('SIGINT', () => close());
  try {
    const corsOrigins = JSON.parse(process.env.TAPE_SHOWCASE_CORS_ORIGINS || '[]');
    corsAllowlist(corsOrigins);
    sandbox = await startSandbox({ onFailure: () => { app?.setUnhealthy(); if (app) void close(true); } });
    if (closing) { await sandbox.close(); process.exitCode = 0; } else {
    app = createShowcase({ sandbox, publicOrigin: process.env.PUBLIC_ORIGIN, corsOrigins,
      allowOpaqueDemoOrigin: process.env.TAPE_SHOWCASE_ALLOW_OPAQUE_DEMO_ORIGIN === '1' });
    app.server.on('error', async () => { await close(true); });
    app.server.listen(Number(process.env.PORT || 18273), process.env.HOST || '127.0.0.1', () => console.log(JSON.stringify({ event: 'ready', port: app.server.address().port, childPids: sandbox.pids, sourceMode: 'isolated_evm' })));
    }
  } catch (error) { console.error(JSON.stringify({ event: 'startup_failed', code: error.code || error.message })); await close(true); }
}
