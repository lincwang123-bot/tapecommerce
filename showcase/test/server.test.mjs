// SPDX-License-Identifier: GPL-3.0-only
import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { createShowcase } from '../server.mjs';
import { storefrontRoutes } from '../static-assets.mjs';
const { Wallet } = createRequire(new URL('../../platform/package.json', import.meta.url))('ethers');

async function fixture(t, options = {}) {
  let clock = Date.now(), sequence = 0, pays = 0, claims = 0, submitFailures = 0, submitted = new Set();
  const records = new Map(), invoices = new Map(), signer = Wallet.createRandom();
  const sandbox = { tenant: 'demo', signer, merchant: Wallet.createRandom().address, checkout: Wallet.createRandom().address,
    close: async () => {}, payer: async () => Wallet.createRandom(),
    async pay(wallet, quote) { pays++; return { txHash: '0x' + 'ab'.repeat(32), blockNumber: 10, blockHash: '0x' + 'cd'.repeat(32) }; },
    async call(route, data) {
      if (route === '/v1/catalog') return { products: [{ id: 1, amountUSDT: '1.00' }] };
      if (route === '/v1/buyers/new') return { buyerId: ++sequence };
      if (route === '/v1/orders/create') { const order = { ...data, orderId: sequence, tenant: 'demo', currency: 'USDT', amountUSDT: '1.00', status: 'pending' }; records.set(sequence, order); return order; }
      if (route === '/v1/quotes/prepare') return records.get(data.orderId);
      if (route === '/v1/quotes/persist') { invoices.set(data.invoice.invoiceId, data); return { state: 'quote_issued' }; }
      if (route === '/v1/invoices/submit') { if (submitFailures-- > 0) throw Object.assign(new Error('bridge_unavailable'), { code: 'bridge_unavailable', status: 503 }); submitted.add(data.invoiceId); }
      if (route === '/v1/invoices/submit' || route === '/v1/invoices/status') return { state: submitted.has(data.invoiceId) ? 'delivered' : 'quote_issued', safe: true, blockNumber: 10 };
      if (route === '/v1/invoices/claim') { claims++; return { payload: 'SYNTHETIC-LICENSE' }; }
      throw new Error('unexpected_route');
    },
  };
  const rates = { latest: async () => ({ source: 'okx', instrumentId: 'OKB-USDT', priceField: 'last', rateUSDTPerOKB: '100', sourceTimestampMs: clock, fetchedAtMs: clock }) };
  const app = createShowcase({ sandbox, rates, now: () => clock, lookup: async ({ processorNumber, tokenId }) => ({ name: `${tokenId}.2.${processorNumber}.tape` }), ...options });
  await new Promise(resolve => app.server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${app.server.address().port}`;
  t.after(async () => {
    await app.close();
    assert.equal(app.server.listening, false);
    await assert.rejects(fetch(base + '/healthz', { signal: AbortSignal.timeout(1000) }));
  });
  const request = async (url, options = {}) => {
    const response = await fetch(base + url, { ...options, headers: { 'content-type': 'application/json', ...options.headers } });
    return { status: response.status, headers: response.headers, data: await response.json() };
  };
  const create = async () => (await request('/api/demo/orders', { method: 'POST', body: JSON.stringify({ productId: '1' }) })).data;
  const orderRequest = (value, action = '', method = 'GET', body = {}) => request(`/api/demo/orders/${value.order.id}${action ? '/' + action : ''}`, { method, headers: { 'x-demo-token': value.token }, ...(method === 'POST' ? { body: JSON.stringify(body) } : {}) });
  return { base, request, create, orderRequest, invoices, pays: () => pays, claims: () => claims, advance: ms => { clock += ms; }, loseSubmit: () => { submitFailures = 1; } };
}

test('serves the six existing templates and assets, keeping operational web files private', async t => {
  const f = await fixture(t);
  for (const [url, [, type]] of storefrontRoutes) {
    const response = await fetch(f.base + url);
    assert.equal(response.status, 200, url);
    assert.equal(response.headers.get('content-type'), type, url);
    assert.ok((await response.arrayBuffer()).byteLength > 0, url);
  }
  for (const url of ['/api.js', '/web/app.js', '/.env', '/storefront-ui/../api.js', '/storefront-ui/%2e%2e%2fapi.js', '/factory-storefront.js', '/tapecommerce-checkout.js']) {
    assert.equal((await fetch(f.base + url)).status, 404, url);
  }
  const head = await fetch(f.base + '/storefront-ui/assets/minimal-768.webp', { method: 'HEAD' });
  assert.equal(head.status, 200);
  assert.equal((await head.arrayBuffer()).byteLength, 0);
});

test('checkout binds authoritative amount, denies early/cross-session delivery and pays once', async t => {
  const f = await fixture(t), a = await f.create(), b = await f.create();
  assert.equal((await f.orderRequest(a, 'delivery')).status, 409);
  assert.equal((await f.orderRequest({ ...a, token: b.token })).status, 404);
  assert.equal((await f.request(`/api/demo/orders/${a.order.id}?token=${a.token}`)).status, 404);
  const quoted = await f.orderRequest(a, 'quote', 'POST', { amountUSDT: '0.00', amountWei: '1', merchant: Wallet.createRandom().address });
  assert.equal(quoted.status, 200);
  assert.equal(quoted.data.order.amountWei, '10000000000000000');
  assert.equal(f.invoices.size, 1);
  assert.equal((await f.orderRequest(a, 'pay', 'POST')).data.order.status, 'delivered');
  assert.equal((await f.orderRequest(a, 'pay', 'POST')).data.order.status, 'delivered');
  assert.equal(f.pays(), 1);
  const delivery = await f.orderRequest(a, 'delivery');
  assert.equal(delivery.status, 200);
  assert.equal(delivery.data.licenseCode, 'SYNTHETIC-LICENSE');
  assert.match(Buffer.from(delivery.data.base64, 'base64').toString(), /Launch checklist/);
  assert.equal(f.claims(), 1);
});

test('expired immutable invoice cannot be paid or replaced', async t => {
  const f = await fixture(t), a = await f.create();
  await f.orderRequest(a, 'quote', 'POST'); f.advance(61000);
  assert.equal((await f.orderRequest(a, 'pay', 'POST')).status, 409);
  assert.equal((await f.orderRequest(a, 'quote', 'POST')).data.error, 'order_quote_expired_create_new');
  assert.equal(f.pays(), 0);
  assert.equal(f.invoices.size, 1);
});

test('lost bridge notification recovers on status polling without a second payment', async t => {
  const f = await fixture(t), a = await f.create();
  await f.orderRequest(a, 'quote', 'POST'); f.loseSubmit();
  assert.equal((await f.orderRequest(a, 'pay', 'POST')).status, 503);
  assert.equal((await f.orderRequest(a)).data.order.status, 'delivered');
  assert.equal((await f.orderRequest(a, 'delivery')).status, 200);
  assert.equal(f.pays(), 1);
});

test('cross-site requests, oversized bodies and request bursts are rejected', async t => {
  const f = await fixture(t);
  assert.equal((await f.request('/api/demo/orders', { method: 'POST', body: '{}', headers: { origin: 'https://evil.invalid' } })).status, 403);
  assert.equal((await f.request('/api/demo/orders', { method: 'POST', body: 'x'.repeat(5000) })).status, 413);
  for (let n = 0; n < 4; n++) assert.ok((await f.create()).order);
  assert.equal((await f.request('/api/demo/orders', { method: 'POST', body: '{"productId":"1"}' })).status, 429);
});

test('configuration never exposes simulator addresses as mainnet deployment', async t => {
  const f = await fixture(t), { data } = await f.request('/api/config');
  assert.equal(data.live.ready, false);
  assert.equal(data.live.checkoutAddress, null);
  assert.equal(data.sourceMode, 'isolated_evm');
  const identity = await f.request('/api/identity?processorNumber=2&tokenId=1');
  assert.equal(identity.data.provenance, 'xlayer_mainnet_readonly');
  assert.equal(identity.data.sampleIsThirdParty, true);
});

test('CORS defaults off and rejects noncanonical or permissive configuration before listening', async t => {
  for (const corsOrigins of ['https://gateway.example', null, ['*'], ['null'], ['http://gateway.example'],
    ['https://gateway.example/'], ['https://gateway.example/path'], ['https://gateway.example?x=1'],
    ['https://gateway.example#x'], ['https://user:pass@gateway.example'], ['https://*.example'],
    ['https://gateway.example', 'https://gateway.example'], Array(17).fill('https://gateway.example')]) {
    assert.throws(() => createShowcase({ corsOrigins }), /invalid_cors_origins/);
  }
  const f = await fixture(t);
  const response = await fetch(f.base + '/api/config', { headers: { origin: 'https://gateway.example', 'sec-fetch-site': 'cross-site' } });
  assert.equal(response.status, 403);
  assert.equal(response.headers.get('access-control-allow-origin'), null);
  const preflight = await fetch(f.base + '/api/config', { method: 'OPTIONS', headers: { origin: 'https://gateway.example', 'access-control-request-method': 'GET' } });
  assert.equal(preflight.status, 405);
  assert.equal(preflight.headers.get('access-control-allow-origin'), null);
});

test('only exact configured gateway origins can preflight the necessary API method and headers', async t => {
  const origin = 'https://123-1-0.gateway.example';
  const f = await fixture(t, { corsOrigins: [origin] });
  const preflight = (path, method = 'GET', requestHeaders = 'X-Demo-Token, Content-Type', extra = {}) => fetch(f.base + path, {
    method: 'OPTIONS', headers: { origin, 'sec-fetch-site': 'cross-site', 'access-control-request-method': method,
      'access-control-request-headers': requestHeaders, ...extra },
  });
  const good = await preflight('/api/config');
  assert.equal(good.status, 204);
  assert.equal(await good.text(), '');
  assert.equal(good.headers.get('access-control-allow-origin'), origin);
  assert.equal(good.headers.get('access-control-allow-methods'), 'GET');
  assert.equal(good.headers.get('access-control-allow-headers'), 'Accept, Content-Type, X-Demo-Token');
  assert.equal(good.headers.get('vary'), 'Origin');
  assert.equal(good.headers.get('access-control-allow-credentials'), null);
  assert.equal(good.headers.get('set-cookie'), null);
  assert.equal((await preflight('/api/demo/orders', 'POST')).status, 204);
  assert.equal((await preflight('/api/config', 'POST')).status, 405);
  assert.equal((await preflight('/api/config', 'DELETE')).status, 405);
  assert.equal((await preflight('/api/not-a-route')).status, 404);
  for (const requested of ['Authorization', 'Cookie', 'X-Forwarded-For', '*', 'Content-Type,']) {
    const response = await preflight('/api/config', 'GET', requested);
    assert.equal(response.status, 403, requested);
    assert.equal(response.headers.get('access-control-allow-origin'), null, requested);
  }
  for (const otherOrigin of ['null', '*', 'https://evil.example', origin + '.evil.example', origin + '/', 'http://123-1-0.gateway.example']) {
    const response = await preflight('/api/config', 'GET', 'Content-Type', { origin: otherOrigin });
    assert.equal(response.status, 403, otherOrigin);
    assert.equal(response.headers.get('access-control-allow-origin'), null, otherOrigin);
  }
  assert.equal((await f.request('/api/config', { headers: { 'sec-fetch-site': 'cross-site' } })).status, 403);
  assert.equal((await f.request('/api/config', { headers: { origin: f.base, 'sec-fetch-site': 'cross-site' } })).status, 403);
  for (const header of ['cookie', 'authorization']) {
    assert.equal((await f.request('/api/config', { headers: { origin, [header]: 'not-an-auth-channel' } })).status, 403);
  }
  const sameOrigin = await f.request('/api/config', { headers: { origin: f.base, 'sec-fetch-site': 'same-origin' } });
  assert.equal(sameOrigin.status, 200);
  assert.equal(sameOrigin.headers.get('access-control-allow-origin'), null);
});

test('allowed gateway checkout retains token isolation, authoritative pricing and live-off response schema', async t => {
  const origin = 'https://123-1-0.gateway.example', f = await fixture(t, { corsOrigins: [origin] });
  const request = (path, method = 'GET', token, body) => f.request(path, {
    method, headers: { origin, 'sec-fetch-site': 'cross-site', ...(token ? { 'x-demo-token': token } : {}) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  const config = await request('/api/config');
  assert.equal(config.headers.get('access-control-allow-origin'), origin);
  assert.equal(config.headers.get('access-control-allow-credentials'), null);
  assert.equal(config.data.live.ready, false);
  assert.equal(config.data.live.checkoutAddress, null);
  assert.equal(config.data.sourceMode, 'isolated_evm');
  assert.equal((await request('/api/catalog')).data.products[0].amountUSDT, '1.00');
  const created = await request('/api/demo/orders', 'POST', undefined, { productId: '1' });
  assert.equal(created.status, 201);
  const { order, token } = created.data, path = `/api/demo/orders/${order.id}`;
  assert.equal((await request(path)).status, 404);
  const quote = await request(path + '/quote', 'POST', token, { amountUSDT: '0.00' });
  assert.equal(quote.status, 200);
  assert.equal(quote.data.order.amountWei, '10000000000000000');
  assert.equal((await request(path + '/pay', 'POST', token, {})).data.order.status, 'delivered');
  assert.equal((await request(path + '/pay', 'POST', token, {})).data.order.status, 'delivered');
  assert.equal(f.pays(), 1);
  assert.equal((await request(path + '/delivery', 'GET', token)).data.licenseCode, 'SYNTHETIC-LICENSE');
});

test('opaque demo access requires a separate opt-in and never exposes formal routes, health or cookie authentication', async t => {
  for (const allowOpaqueDemoOrigin of [null, 1, '1', 'true', {}]) {
    assert.throws(() => createShowcase({ allowOpaqueDemoOrigin }), /invalid_opaque_demo_origin_option/);
  }
  assert.throws(() => createShowcase({ allowOpaqueDemoOrigin: true, corsOrigins: ['null'] }), /invalid_cors_origins/);
  const disabled = await fixture(t);
  const rejected = await disabled.request('/api/config', { headers: { origin: 'null', 'sec-fetch-site': 'cross-site' } });
  assert.equal(rejected.status, 403);
  assert.equal(rejected.headers.get('access-control-allow-origin'), null);
  const f = await fixture(t, { allowOpaqueDemoOrigin: true });
  const headers = { origin: 'null', 'sec-fetch-site': 'cross-site' };
  const preflight = await fetch(f.base + '/api/demo/orders', { method: 'OPTIONS', headers: { ...headers,
    'access-control-request-method': 'POST', 'access-control-request-headers': 'content-type,x-demo-token' } });
  assert.equal(preflight.status, 204);
  assert.equal(preflight.headers.get('access-control-allow-origin'), 'null');
  assert.equal(preflight.headers.get('access-control-allow-credentials'), null);
  assert.equal(preflight.headers.get('vary'), 'Origin');
  const config = await f.request('/api/config', { headers });
  assert.equal(config.status, 200);
  assert.equal(config.data.sourceMode, 'isolated_evm');
  assert.equal(config.data.live.ready, false);
  assert.equal(config.data.live.checkoutAddress, null);
  for (const route of ['/api/v1/public/tapecommerce/config', '/api/v1/orders/ORDER/tapecommerce/quote', '/healthz', '/', '/rpc', '/api/not-a-route']) {
    for (const method of ['GET', 'OPTIONS']) {
      const response = await fetch(f.base + route, { method, headers: { ...headers, 'access-control-request-method': 'GET' } });
      assert.equal(response.status, 403, `${method} ${route}`);
      assert.equal(response.headers.get('access-control-allow-origin'), null, `${method} ${route}`);
    }
  }
  assert.equal((await fetch(f.base + '/healthz')).status, 200);
  for (const bad of [{ cookie: 'session=not-auth' }, { authorization: 'Bearer not-auth' }, { origin: 'https://evil.example' }, { origin: 'Null' }]) {
    const response = await f.request('/api/config', { headers: { ...headers, ...bad } });
    assert.equal(response.status, 403);
    assert.equal(response.headers.get('access-control-allow-origin'), null);
  }
  assert.equal((await f.request('/api/config', { headers: { 'sec-fetch-site': 'cross-site' } })).status, 403);
  const badPreflight = await fetch(f.base + '/api/config', { method: 'OPTIONS', headers: { ...headers,
    'access-control-request-method': 'GET', 'access-control-request-headers': 'authorization' } });
  assert.equal(badPreflight.status, 403);
  assert.equal(badPreflight.headers.get('access-control-allow-origin'), null);
  const a = await f.create(), b = await f.create();
  const route = `/api/demo/orders/${a.order.id}`;
  for (const token of [undefined, b.token, '0'.repeat(64)]) {
    assert.equal((await f.request(route, { headers: { ...headers, ...(token ? { 'x-demo-token': token } : {}) } })).status, 404);
  }
  assert.equal((await f.request(route + '?token=' + a.token, { headers })).status, 404);
  const authorized = await f.request(route, { headers: { ...headers, 'x-demo-token': a.token } });
  assert.equal(authorized.status, 200);
  assert.equal(authorized.headers.get('access-control-allow-origin'), 'null');
  assert.equal(authorized.headers.get('access-control-allow-credentials'), null);
});
