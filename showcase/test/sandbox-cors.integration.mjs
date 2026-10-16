// SPDX-License-Identifier: GPL-3.0-only
// Explicit integration command; needs an already compiled local Go demo binary.
// Never runs against the persistent showcase or a mainnet transaction endpoint.
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, readdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { startSandbox } from '../sandbox.mjs';
import { createShowcase } from '../server.mjs';

test('real isolated EVM and original Go stock flow work for same-origin, allowed gateway and opt-in opaque demo requests', { timeout: 90000 }, async t => {
  assert.ok(process.env.TAPECOMMERCE_BRIDGE_BIN, 'Set TAPECOMMERCE_BRIDGE_BIN to a newly built local cmd/tapecommerce-demo');
  const root = await mkdtemp(path.join(tmpdir(), 'tape-cors-integration-'));
  const previousDataDir = process.env.TAPECOMMERCE_DATA_DIR;
  process.env.TAPECOMMERCE_DATA_DIR = root;
  const pids = [], flows = [];
  let sandbox, app, base, failedChild = false;
  t.after(async () => {
    try {
      if (app) await app.close(); else await sandbox?.close();
      if (app) {
        assert.equal(app.server.listening, false);
        await assert.rejects(fetch(base + '/healthz', { signal: AbortSignal.timeout(1000) }));
      }
      for (const pid of pids) assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
      assert.deepEqual(await readdir(root), []);
      console.log(JSON.stringify({ evidence: 'sandbox_cors_cleanup', childPids: pids, exited: true, listenerClosed: true, temporaryDatabaseRemoved: true }));
    } finally {
      if (previousDataDir === undefined) delete process.env.TAPECOMMERCE_DATA_DIR;
      else process.env.TAPECOMMERCE_DATA_DIR = previousDataDir;
      await rm(root, { recursive: true, force: true });
    }
  });
  sandbox = await startSandbox({ onProcess: pid => pids.push(pid), onFailure: () => { failedChild = true; } });
  const gateway = 'https://123-1-0.gateway.example';
  const rates = { async latest() { const now = Date.now(); return { source: 'okx', instrumentId: 'OKB-USDT', priceField: 'last', rateUSDTPerOKB: '117.91', sourceTimestampMs: now, fetchedAtMs: now }; } };
  app = createShowcase({ sandbox, rates, corsOrigins: [gateway], allowOpaqueDemoOrigin: true });
  await new Promise(resolve => app.server.listen(0, '127.0.0.1', resolve));
  base = `http://127.0.0.1:${app.server.address().port}`;
  const previousTokens = [];
  for (const mode of ['same_origin', 'allowlisted_cross_origin', 'opt_in_opaque_demo']) {
    const origin = mode === 'same_origin' ? base : mode === 'opt_in_opaque_demo' ? 'null' : gateway;
    const commonHeaders = { origin, 'sec-fetch-site': mode === 'same_origin' ? 'same-origin' : 'cross-site' };
    const request = async (route, body, token, expected = 200) => {
      const response = await fetch(base + route, {
        method: body === undefined ? 'GET' : 'POST', headers: { ...commonHeaders,
          ...(body === undefined ? {} : { 'content-type': 'application/json' }), ...(token ? { 'x-demo-token': token } : {}) },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }), credentials: 'omit', redirect: 'error', signal: AbortSignal.timeout(25000),
      });
      assert.equal(response.status, expected, `${mode}: unexpected status`);
      assert.equal(response.headers.get('access-control-allow-origin'), mode === 'same_origin' ? null : origin);
      assert.equal(response.headers.get('access-control-allow-credentials'), null);
      return response.json();
    };
    if (mode !== 'same_origin') {
      const preflight = await fetch(base + '/api/demo/orders', { method: 'OPTIONS', headers: { ...commonHeaders, 'access-control-request-method': 'POST', 'access-control-request-headers': 'content-type,x-demo-token' } });
      assert.equal(preflight.status, 204);
      assert.equal(preflight.headers.get('access-control-allow-origin'), origin);
    }
    const config = await request('/api/config');
    assert.equal(config.live.ready, false);
    assert.equal(config.sourceMode, 'isolated_evm');
    const catalog = await request('/api/catalog');
    const created = await request('/api/demo/orders', { productId: catalog.products[0].id }, undefined, 201);
    const route = `/api/demo/orders/${created.order.id}`;
    await request(route, undefined, '0'.repeat(64), 404);
    for (const token of previousTokens) await request(route, undefined, token, 404);
    previousTokens.push(created.token);
    await request(route + '/delivery', undefined, created.token, 409);
    const quoted = await request(route + '/quote', {}, created.token);
    assert.equal(quoted.order.amountUSDT, '1.00');
    const paid = await request(route + '/pay', {}, created.token);
    assert.equal(paid.order.status, 'delivered');
    assert.equal(paid.order.receipt.safe, true);
    assert.match(paid.order.txHash, /^0x[a-f0-9]{64}$/);
    const replay = await request(route + '/pay', {}, created.token);
    assert.equal(replay.order.txHash, paid.order.txHash);
    const delivery = await request(route + '/delivery', undefined, created.token);
    assert.equal(delivery.filename, 'tapecommerce-store-kit.html');
    assert.ok(delivery.licenseCode);
    assert.ok(Buffer.from(delivery.base64, 'base64').length > 100);
    flows.push({ mode, state: paid.order.status, safe: true, transactionHash: paid.order.txHash, replaySameTransaction: true, authorizedDelivery: true });
  }
  assert.equal(failedChild, false);
  assert.equal(new Set(flows.map(flow => flow.transactionHash)).size, flows.length);
  console.log(JSON.stringify({ evidence: 'sandbox_cors_flows', chainId: 196, sourceMode: 'isolated_evm', rates: 'controlled_test_snapshot', realAnvilAndGo: true,
    requestTransport: 'loopback HTTP with explicit test Origin; not a deployed browser gateway', liveReady: false, flows }));
});
