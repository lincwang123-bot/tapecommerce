// SPDX-License-Identifier: GPL-3.0-only
import { randomBytes } from 'node:crypto';
import { getAddress, keccak256, toUtf8Bytes } from 'ethers';
import { fail } from './errors.mjs';
import { FX_POLICY_VERSION, QUOTE_TTL_SECONDS, usdtToOKBWei, validateRate } from './fx.mjs';
import { okxSpotRates } from './okx-rate.mjs';

export const invoiceTypes = Object.freeze({ Invoice: Object.freeze([
  { name: 'invoiceId', type: 'bytes32' }, { name: 'tenantHash', type: 'bytes32' },
  { name: 'payer', type: 'address' }, { name: 'merchant', type: 'address' },
  { name: 'amountWei', type: 'uint256' }, { name: 'deadline', type: 'uint64' },
].map(Object.freeze)) });
// Explicit decimal conversion. Prices are never derived from floating point numbers.
export function okbToWei(value) {
  if (typeof value !== 'string' || !/^(0|[1-9][0-9]*)(\.[0-9]{1,18})?$/.test(value) || value.length > 80) fail('invalid_okb_price');
  const [whole, fraction = ''] = value.split('.');
  const wei = BigInt(whole) * 10n ** 18n + BigInt(fraction.padEnd(18, '0'));
  if (wei <= 0n || wei >= 2n ** 256n) fail('invalid_okb_price');
  return wei.toString();
}
// Internal port only. No HTTP route accepts a browser-supplied amount/merchant/order snapshot.
// The integration must persist this exact invoice before exposing it to a buyer.
export async function signTrustedOrder({ orderId, buyer, tenant, orders, identities, signer, checkoutContract, rates = okxSpotRates, now = Date.now }) {
  const loadedOrder = await orders.loadPayable({ orderId, buyer, tenant });
  if (!loadedOrder || loadedOrder.id !== orderId || loadedOrder.tenant !== tenant || loadedOrder.buyer !== buyer || loadedOrder.currency !== 'USDT' || loadedOrder.status !== 'pending') fail('order_not_payable', 409);
  const order = { amountUSDT: loadedOrder.amountUSDT, payer: loadedOrder.payer };
  const identity = await identities.verifyCurrent(tenant);
  if (!identity || identity.chainId !== 196 || identity.status !== 'verified' || !identity.live) fail('tape_identity_required', 409);
  const merchant = getAddress(identity.holder), contract = getAddress(checkoutContract), payer = getAddress(order.payer);
  if ([merchant, contract, payer].some(a => /^0x0{40}$/i.test(a))) fail('invalid_checkout_address');
  if (merchant === contract) fail('invalid_checkout_address');
  const rate = await rates.latest();
  const issuedAtMs = now();
  const fxSnapshot = Object.freeze({ ...validateRate(rate, issuedAtMs), amountUSDT: order.amountUSDT, rounding: 'ceil_wei',
    policyVersion: FX_POLICY_VERSION, spreadBps: 0, issuedAtMs });
  const domain = Object.freeze({ name: 'TapeCommerceCheckout', version: '1', chainId: 196, verifyingContract: contract });
  const invoice = Object.freeze({ invoiceId: '0x' + randomBytes(32).toString('hex'), tenantHash: keccak256(toUtf8Bytes(tenant)), payer, merchant,
    amountWei: usdtToOKBWei(order.amountUSDT, fxSnapshot.rateUSDTPerOKB), deadline: String(Math.floor(issuedAtMs / 1000) + QUOTE_TTL_SECONDS) });
  const signature = await signer.signTypedData(domain, invoiceTypes, invoice);
  const completedAtMs = now();
  validateRate(fxSnapshot, completedAtMs);
  if (completedAtMs < issuedAtMs || Math.floor(completedAtMs / 1000) >= Number(invoice.deadline)) fail('fx_quote_expired', 503);
  return { domain, types: invoiceTypes, invoice, signature, fxSnapshot };
}
