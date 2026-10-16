// SPDX-License-Identifier: GPL-3.0-only
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { id } from 'ethers';
import { ROOT } from './compile.mjs';
import { environment, INVOICE_TYPES } from '../test/helpers.mjs';

const rpcUrl = process.env.XLAYER_RPC_URL ?? 'https://rpc.xlayer.tech';
const evidencePath = path.join(ROOT, 'evidence/fork-smoke.json');
let env;
const evidence = {
  runAt: new Date().toISOString(),
  method: 'Read-only remote fork; deployment/payment exist solely in an in-memory local overlay',
  rpcOrigin: new URL(rpcUrl).origin,
  mainnetTransactionsSent: 0,
  status: 'running',
  providerDisconnected: null,
};

async function rpc(method, params = []) {
  const response = await fetch(rpcUrl, {
    method: 'POST', headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }),
    signal: AbortSignal.timeout(15_000),
  });
  if (!response.ok) throw new Error(`${method}: HTTP ${response.status}`);
  const json = await response.json();
  if (json.error) throw new Error(`${method}: ${JSON.stringify(json.error)}`);
  return json.result;
}

try {
  assert.equal(BigInt(await rpc('eth_chainId')), 196n);
  const block = await rpc('eth_getBlockByNumber', ['latest', false]);
  evidence.forkBlockNumber = Number(BigInt(block.number));
  evidence.forkBlockHash = block.hash;
  // Ganache fetches remote state but executes every write on its local overlay.
  env = await environment({ fork: { url: rpcUrl, blockNumber: evidence.forkBlockNumber, disableCache: true } });
  const checkout = await env.deploy('TapeCommerceCheckout', [await env.owner.getAddress(), env.quoteSigner.address]);
  const localDeployment = await checkout.deploymentTransaction().wait();
  const domain = { name: 'TapeCommerceCheckout', version: '1', chainId: 196, verifyingContract: await checkout.getAddress() };
  const latest = await env.provider.getBlock('latest');
  const invoice = {
    invoiceId: id('local-only-fork-smoke-invoice'), tenantHash: id('local-only-fork-smoke-tenant'),
    payer: await env.payer.getAddress(), merchant: await env.merchant.getAddress(),
    amountWei: 10n ** 15n, deadline: BigInt(latest.timestamp + 300),
  };
  const signature = await env.quoteSigner.signTypedData(domain, INVOICE_TYPES, invoice);
  const before = await env.provider.getBalance(invoice.merchant);
  const receipt = await (await checkout.connect(env.payer).payOKB(invoice, signature, { value: invoice.amountWei })).wait();
  assert.equal(receipt.status, 1);
  assert.equal(await checkout.paidInvoices(invoice.invoiceId), true);
  assert.equal(await env.provider.getBalance(invoice.merchant), before + invoice.amountWei);
  assert.equal(await env.provider.getBalance(await checkout.getAddress()), 0n);
  evidence.status = 'passed';
  evidence.localOnly = { deploymentGasUnits: localDeployment.gasUsed.toString(), paymentGasUnits: receipt.gasUsed.toString() };
} catch (error) {
  evidence.status = 'blocked';
  evidence.reason = error.shortMessage ?? error.message;
  process.exitCode = 1;
} finally {
  if (env) { await env.close(); evidence.providerDisconnected = true; }
  fs.mkdirSync(path.dirname(evidencePath), { recursive: true });
  fs.writeFileSync(evidencePath, JSON.stringify(evidence, null, 2) + '\n');
  console.log(JSON.stringify(evidence, null, 2));
}
