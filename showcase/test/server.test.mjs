// SPDX-License-Identifier: GPL-3.0-only
import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { createShowcase } from '../server.mjs';
import { storefrontRoutes } from '../static-assets.mjs';
const { Wallet } = createRequire(new URL('../../platform/package.json', import.meta.url))('ethers');

async function fixture(t) {
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
  const app = createShowcase({ sandbox, rates, now: () => clock, lookup: async ({ processorNumber, tokenId }) => ({ name: `${tokenId}.2.${processorNumber}.tape` }) });
  await new Promise(resolve => app.server.listen(0, '127.0.0.1', resolve));
  t.after(() => app.close());
  const base = `http://127.0.0.1:${app.server.address().port}`;
  const request = async (url, options = {}) => {
    const response = await fetch(base + url, { ...options, headers: { 'content-type': 'application/json', ...options.headers } });
    return { status: response.status, data: await response.json() };
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
