// SPDX-License-Identifier: GPL-3.0-only
import { fail } from './errors.mjs';

export const RATE_MAX_AGE_MS = 30_000;
export const RATE_FUTURE_SKEW_MS = 5_000;
export const QUOTE_TTL_SECONDS = 60;
export const FX_POLICY_VERSION = 'okx_spot_last_v1';
const WEI = 10n ** 18n;
const UINT256_LIMIT = 2n ** 256n;

function rateParts(value) {
  if (typeof value !== 'string' || !/^(0|[1-9][0-9]{0,17})(\.[0-9]{1,18})?$/.test(value)) fail('invalid_fx_rate', 503);
  const [whole, fraction = ''] = value.split('.');
  const coefficient = BigInt(whole + fraction);
  if (coefficient <= 0n) fail('invalid_fx_rate', 503);
  return { coefficient, scale: 10n ** BigInt(fraction.length) };
}

// Matches the existing decimal(20,2) order amount's fixed-string representation.
// The rate is USDT per OKB, so divide; round up by at most one wei, without spread.
export function usdtToOKBWei(amountUSDT, rateUSDTPerOKB) {
  if (typeof amountUSDT !== 'string' || !/^(0|[1-9][0-9]{0,17})\.[0-9]{2}$/.test(amountUSDT)) fail('invalid_usdt_amount');
  const cents = BigInt(amountUSDT.replace('.', ''));
  if (cents <= 0n) fail('invalid_usdt_amount');
  const { coefficient, scale } = rateParts(rateUSDTPerOKB);
  const numerator = cents * WEI * scale;
  const denominator = coefficient * 100n;
  const wei = (numerator + denominator - 1n) / denominator;
  if (wei <= 0n || wei >= UINT256_LIMIT) fail('invalid_fx_amount');
  return wei.toString();
}

export function assertTimestamp(value) {
  if (!Number.isSafeInteger(value) || value <= 0) fail('invalid_fx_timestamp', 503);
}

// Revalidate at the signing boundary, including snapshots from an injected port.
// Local fetchedAt must not be in the future. Only the exchange clock gets skew.
export function validateRate(rate, nowMs) {
  if (!rate || rate.source !== 'okx' || rate.instrumentId !== 'OKB-USDT' || rate.priceField !== 'last') fail('invalid_fx_source', 503);
  rateParts(rate.rateUSDTPerOKB);
  for (const timestamp of [rate.sourceTimestampMs, rate.fetchedAtMs, nowMs]) assertTimestamp(timestamp);
  if (rate.fetchedAtMs > nowMs) fail('invalid_fx_timestamp', 503);
  for (const reference of [rate.fetchedAtMs, nowMs]) {
    if (reference - rate.sourceTimestampMs > RATE_MAX_AGE_MS) fail('fx_rate_stale', 503);
    if (rate.sourceTimestampMs - reference > RATE_FUTURE_SKEW_MS) fail('fx_rate_future', 503);
  }
  return Object.freeze({ source: 'okx', instrumentId: 'OKB-USDT', priceField: 'last',
    rateUSDTPerOKB: rate.rateUSDTPerOKB, sourceTimestampMs: rate.sourceTimestampMs, fetchedAtMs: rate.fetchedAtMs });
}
