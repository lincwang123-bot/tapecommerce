// SPDX-License-Identifier: GPL-3.0-only
import test from 'node:test';
import assert from 'node:assert/strict';
import { Wallet, verifyTypedData } from 'ethers';
import { usdtToOKBWei, validateRate } from '../src/tapecommerce/fx.mjs';
import { createOkxSpotRates, RATE_TIMEOUT_MS } from '../src/tapecommerce/okx-rate.mjs';
import { signTrustedOrder } from '../src/tapecommerce/quote.mjs';

const TIME = 1_792_128_000_000;
const rate = (patch = {}) => ({ source: 'okx', instrumentId: 'OKB-USDT', priceField: 'last',
  rateUSDTPerOKB: '117.91', sourceTimestampMs: TIME, fetchedAtMs: TIME, ...patch });
const row = (patch = {}) => ({ instType: 'SPOT', instId: 'OKB-USDT', last: '117.91', ts: String(TIME), ...patch });
const payload = (patch = {}) => ({ code: '0', data: [row()], ...patch });
const provider = (fetchImpl) => createOkxSpotRates({ fetchImpl, now: () => TIME });

test('USDT conversion divides by price, rounds up only for a remainder and preserves large exact amounts', () => {
  for (const [usdt, price, expected] of [
    ['117.91', '117.91', '1000000000000000000'],
    ['1.00', '3', '333333333333333334'],
    ['1.00', '4.000', '250000000000000000'],
    ['0.01', '999999999999999999.999999999999999999', '1'],
    ['999999999999999999.99', '0.000000000000000001', '999999999999999999990000000000000000000000000000000000'],
    ['9007199254740993.01', '1', '9007199254740993010000000000000000'],
  ]) assert.equal(usdtToOKBWei(usdt, price), expected);
  // A non-divisible payment is sufficient, but removing one wei underpays.
  const wei = BigInt(usdtToOKBWei('29.99', '117.91'));
  assert.ok(wei * 11791n >= 2999n * 10n ** 18n);
  assert.ok((wei - 1n) * 11791n < 2999n * 10n ** 18n);
});

test('conversion refuses lossy, nonpositive, noncanonical and overprecision monetary input', () => {
  for (const value of [1, null, undefined, '1', '1.0', '1.000', '0.00', '-1.00', '+1.00', '01.00', '1e2', ' 1.00', '1.00 ', '1,00', '1' + '0'.repeat(18) + '.00']) {
    assert.throws(() => usdtToOKBWei(value, '117.91'), /invalid_usdt_amount/);
  }
  for (const value of [117.91, null, '0', '0.000', '-1', '+1', '01', '1e2', '.1', '1.', 'Infinity', 'NaN', '0x12', '1' + '0'.repeat(18), '0.' + '1'.repeat(19)]) {
    assert.throws(() => usdtToOKBWei('1.00', value), /invalid_fx_rate/);
  }
});

test('snapshot freshness boundaries distinguish exchange clock skew from local future timestamps', () => {
  for (const age of [29999, 30000, -4999, -5000]) assert.ok(validateRate(rate({ sourceTimestampMs: TIME - age }), TIME));
  assert.throws(() => validateRate(rate({ sourceTimestampMs: TIME - 30001 }), TIME), /stale/);
  assert.throws(() => validateRate(rate({ sourceTimestampMs: TIME + 5001 }), TIME), /future/);
  assert.throws(() => validateRate(rate({ fetchedAtMs: TIME + 1 }), TIME), /timestamp/);
  assert.throws(() => validateRate(rate({ fetchedAtMs: TIME - 5001 }), TIME), /future/);
  for (const value of [0, -1, NaN, Infinity, 1.5, '1792128000000', Number.MAX_SAFE_INTEGER + 1]) {
    assert.throws(() => validateRate(rate({ sourceTimestampMs: value }), TIME), /timestamp/);
  }
  for (const patch of [{ source: 'browser' }, { instrumentId: 'OKB-USD' }, { priceField: 'askPx' }]) {
    assert.throws(() => validateRate(rate(patch), TIME), /source/);
  }
});

test('OKX adapter uses only the fixed public spot endpoint and keeps original rate precision', async () => {
  let requested = 0;
  const rates = provider(async (url, options) => {
    requested++;
    assert.equal(url, 'https://openapi.okx.com/api/v5/market/ticker?instId=OKB-USDT');
    assert.equal(options.redirect, 'error');
    assert.equal(options.credentials, 'omit');
    assert.equal(options.cache, 'no-store');
    assert.equal(Object.keys(options.headers).some(k => /auth|key|passphrase/i.test(k)), false);
    return Response.json(payload({ data: [row({ last: '117.9100' })] }));
  });
  assert.deepEqual(await rates.latest(), rate({ rateUSDTPerOKB: '117.9100' }));
  await rates.latest();
  assert.equal(requested, 2, 'each request reads a fresh response; no price cache');
});

test('bad HTTP or API payloads never produce a rate', async t => {
  const cases = [
    ['HTTP429', () => Response.json(payload(), { status: 429 })],
    ['HTTP503', () => Response.json(payload(), { status: 503 })],
    ['redirect', () => ({ status: 200, redirected: true })],
    ['wrong URL', () => ({ status: 200, url: 'https://other.example/api' })],
    ['non-JSON', () => new Response('<html>blocked</html>')],
    ['invalid JSON', () => new Response('{', { headers: { 'content-type': 'application/json' } })],
    ['oversized body', () => Response.json({ ...payload(), padding: 'x'.repeat(16384) })],
    ['API error', () => Response.json(payload({ code: '50011' }))],
    ['numeric code', () => Response.json(payload({ code: 0 }))],
    ['empty data', () => Response.json(payload({ data: [] }))],
    ['multiple rows', () => Response.json(payload({ data: [row(), row()] }))],
    ['wrong pair', () => Response.json(payload({ data: [row({ instId: 'BTC-USDT' })] }))],
    ['wrong product', () => Response.json(payload({ data: [row({ instType: 'SWAP' })] }))],
    ['zero price', () => Response.json(payload({ data: [row({ last: '0' })] }))],
    ['numeric price', () => Response.json(payload({ data: [row({ last: 117.91 })] }))],
    ['numeric ts', () => Response.json(payload({ data: [row({ ts: TIME })] }))],
    ['seconds ts', () => Response.json(payload({ data: [row({ ts: String(TIME / 1000) })] }))],
    ['unsafe ts', () => Response.json(payload({ data: [row({ ts: String(Number.MAX_SAFE_INTEGER + 1) })] }))],
    ['stale', () => Response.json(payload({ data: [row({ ts: String(TIME - 30001) })] }))],
    ['future', () => Response.json(payload({ data: [row({ ts: String(TIME + 5001) })] }))],
    ['null row', () => Response.json(payload({ data: [null] }))],
  ];
  for (const [name, response] of cases) await t.test(name, async () => {
    await assert.rejects(provider(async () => response()).latest(), error => error.status === 503);
  });
});

test('no stale or cached fallback after success; regressed source timestamps are refused within one provider', async () => {
  let result = row(); let offline = false;
  const rates = provider(async () => {
    if (offline) throw Object.assign(new Error('private upstream detail'), { code: 20 });
    return Response.json(payload({ data: [result] }));
  });
  await rates.latest();
  result = row({ ts: String(TIME - 1) });
  await assert.rejects(rates.latest(), /fx_rate_regressed/);
  offline = true;
  await assert.rejects(rates.latest(), { message: 'fx_source_unavailable' });
  offline = false; result = row();
  assert.equal((await rates.latest()).sourceTimestampMs, TIME);
});

test('five-second timeout covers connection and a stalled JSON body, and aborts the transport', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  for (const bodyStalls of [false, true]) {
    let signal;
    const rates = provider(async (_url, options) => {
      signal = options.signal;
      if (!bodyStalls) return await new Promise((_resolve, reject) => signal.addEventListener('abort', () => reject(signal.reason), { once: true }));
      return new Response(new ReadableStream({ start(controller) {
        controller.enqueue(new TextEncoder().encode('{'));
        signal.addEventListener('abort', () => controller.error(signal.reason), { once: true });
      } }), { headers: { 'content-type': 'application/json' } });
    });
    const pending = rates.latest();
    const rejection = assert.rejects(pending, /fx_source_timeout/);
    await new Promise(setImmediate);
    t.mock.timers.tick(RATE_TIMEOUT_MS);
    await rejection;
    assert.equal(signal.aborted, true);
  }
});

function quoteFixture() {
  let clock = TIME; let quoteRate = rate();
  const wallet = Wallet.createRandom();
  const merchant = Wallet.createRandom().address;
  const contract = Wallet.createRandom().address;
  const order = { id: 'order-1', buyer: 'buyer-1', tenant: 'shop-1', currency: 'USDT', status: 'pending', payer: wallet.address, amountUSDT: '117.91' };
  return { wallet, merchant, order, advance: ms => clock += ms, setRate: r => { quoteRate = r; },
    input: { orderId: order.id, buyer: order.buyer, tenant: order.tenant, orders: { loadPayable: async () => order },
      identities: { verifyCurrent: async () => ({ chainId: 196, holder: merchant, status: 'verified', live: true }) },
      signer: wallet, checkoutContract: contract, rates: { latest: async () => quoteRate }, now: () => clock } };
}

test('signed invoice locks the precise converted amount/deadline and an auditable, immutable FX snapshot', async () => {
  const f = quoteFixture();
  const q = await signTrustedOrder(f.input);
  assert.equal(q.invoice.amountWei, '1000000000000000000');
  assert.equal(q.invoice.deadline, String(TIME / 1000 + 60));
  assert.equal(q.invoice.merchant, f.merchant);
  assert.deepEqual(q.fxSnapshot, { ...rate(), amountUSDT: '117.91', issuedAtMs: TIME, rounding: 'ceil_wei', policyVersion: 'okx_spot_last_v1', spreadBps: 0 });
  assert.equal(verifyTypedData(q.domain, q.types, q.invoice, q.signature), f.wallet.address);
  assert.notEqual(verifyTypedData(q.domain, q.types, { ...q.invoice, amountWei: '1000000000000000001' }, q.signature), f.wallet.address);
  assert.notEqual(verifyTypedData(q.domain, q.types, { ...q.invoice, deadline: String(TIME / 1000 + 61) }, q.signature), f.wallet.address);
  assert.throws(() => { q.fxSnapshot.amountUSDT = '0.01'; }, TypeError);
  assert.throws(() => { q.invoice.deadline = '9999999999'; }, TypeError);
});

test('quote rejects stale rates after asynchronous holder lookup and after slow signing', async () => {
  const f = quoteFixture();
  const identityRead = f.input.identities.verifyCurrent;
  f.input.identities.verifyCurrent = async () => { f.advance(30001); return identityRead(); };
  await assert.rejects(signTrustedOrder(f.input), /fx_rate_stale/);

  for (const elapsed of [30001, 60000]) {
    const slow = quoteFixture();
    slow.input.signer = { signTypedData: async (...args) => { const signature = await slow.wallet.signTypedData(...args); slow.advance(elapsed); return signature; } };
    await assert.rejects(signTrustedOrder(slow.input), /fx_rate_stale|fx_quote_expired/);
  }
});

test('signing delay never extends deadline; a backward clock and signer failure return no quote', async () => {
  const f = quoteFixture();
  f.input.signer = { signTypedData: async (...args) => { f.advance(10000); return f.wallet.signTypedData(...args); } };
  const q = await signTrustedOrder(f.input);
  assert.equal(q.invoice.deadline, String(TIME / 1000 + 60));
  const backwards = quoteFixture();
  backwards.input.signer = { signTypedData: async () => { backwards.advance(-1); return 'unused'; } };
  await assert.rejects(signTrustedOrder(backwards.input), /timestamp|expired/);
  await assert.rejects(signTrustedOrder({ ...quoteFixture().input, signer: { signTypedData: async () => { throw Error('signing_failed'); } } }), /signing_failed/);
});

test('non-integral second issuance floors deadline; exactly 30 seconds of signing remains fresh', async () => {
  const f = quoteFixture();
  f.advance(999);
  f.setRate(rate({ sourceTimestampMs: TIME + 999, fetchedAtMs: TIME + 999 }));
  f.input.signer = { signTypedData: async (...args) => { f.advance(30000); return f.wallet.signTypedData(...args); } };
  const q = await signTrustedOrder(f.input);
  assert.equal(q.fxSnapshot.issuedAtMs, TIME + 999);
  assert.equal(q.invoice.deadline, String(TIME / 1000 + 60));
});

test('signing port cannot substitute buyer, tenant, order, currency, price source or checkout recipient', async () => {
  for (const patch of [{ buyer: 'other' }, { tenant: 'other' }, { orderId: 'other' }]) {
    await assert.rejects(signTrustedOrder({ ...quoteFixture().input, ...patch }), /not_payable/);
  }
  for (const patch of [{ currency: 'OKB' }, { status: 'paid' }, { amountUSDT: 117.91 }]) {
    const f = quoteFixture(); Object.assign(f.order, patch);
    await assert.rejects(signTrustedOrder(f.input), /not_payable|invalid_usdt/);
  }
  const f = quoteFixture(); f.setRate(rate({ source: 'browser' }));
  await assert.rejects(signTrustedOrder(f.input), /invalid_fx_source/);
  const same = quoteFixture();
  await assert.rejects(signTrustedOrder({ ...same.input, checkoutContract: same.merchant }), /invalid_checkout_address/);
});
