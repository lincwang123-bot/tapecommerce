// SPDX-License-Identifier: GPL-3.0-only
import { fail } from './errors.mjs';
import { validateRate } from './fx.mjs';

export const OKX_TICKER_URL = 'https://openapi.okx.com/api/v5/market/ticker?instId=OKB-USDT';
export const RATE_TIMEOUT_MS = 5_000;
const MAX_RESPONSE_BYTES = 16_384;

// No API key, configurable host, retries, cached-price fallback or browser input.
// The injectable transport/clock are internal test ports, never HTTP parameters.
export function createOkxSpotRates({ fetchImpl = globalThis.fetch, now = Date.now } = {}) {
  let newestTimestamp = 0;
  return Object.freeze({ async latest() {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), RATE_TIMEOUT_MS);
    let response;
    try {
      response = await fetchImpl(OKX_TICKER_URL, { method: 'GET', redirect: 'error', credentials: 'omit', cache: 'no-store',
        headers: { accept: 'application/json', 'cache-control': 'no-cache' }, signal: controller.signal });
      if (response.status !== 200 || response.redirected || (response.url && response.url !== OKX_TICKER_URL)) fail('fx_source_unavailable', 503);
      if (!/^application\/json(?:\s*;|$)/i.test(response.headers.get('content-type') || '') || !response.body) fail('invalid_fx_response', 503);
      const chunks = []; let size = 0;
      for await (const chunk of response.body) {
        size += chunk.byteLength;
        if (size > MAX_RESPONSE_BYTES) fail('invalid_fx_response', 503);
        chunks.push(chunk);
      }
      const payload = JSON.parse(Buffer.concat(chunks).toString('utf8'));
      if (payload?.code !== '0' || !Array.isArray(payload.data) || payload.data.length !== 1) fail('invalid_fx_response', 503);
      const row = payload.data[0];
      if (row?.instType !== 'SPOT' || row.instId !== 'OKB-USDT' || typeof row.ts !== 'string' || !/^[1-9][0-9]{0,15}$/.test(row.ts)) fail('invalid_fx_response', 503);
      const fetchedAtMs = now();
      const rate = validateRate({ source: 'okx', instrumentId: row.instId, priceField: 'last', rateUSDTPerOKB: row.last,
        sourceTimestampMs: Number(row.ts), fetchedAtMs }, fetchedAtMs);
      // OKX documents independent caches: later requests can return an older ts.
      // Reject backwards movement in this provider instance; never reuse a price.
      if (rate.sourceTimestampMs < newestTimestamp) fail('fx_rate_regressed', 503);
      newestTimestamp = rate.sourceTimestampMs;
      return rate;
    } catch (error) {
      if (controller.signal.aborted) fail('fx_source_timeout', 503);
      if (typeof error?.code === 'string' && (error.code.startsWith('fx_') || error.code.startsWith('invalid_fx_'))) throw error;
      fail('fx_source_unavailable', 503);
    } finally {
      clearTimeout(timeout);
      controller.abort();
      if (response?.body && !response.bodyUsed) await response.body.cancel().catch(() => {});
    }
  } });
}

export const okxSpotRates = createOkxSpotRates();
