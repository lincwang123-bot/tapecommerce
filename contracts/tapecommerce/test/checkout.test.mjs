// SPDX-License-Identifier: GPL-3.0-only
import test, { after } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { Contract, Interface, TypedDataEncoder, ZeroAddress, ZeroHash, id, concat, toBeHex } from 'ethers';
import { ROOT } from '../scripts/compile.mjs';
import { artifact, environment, fixture, INVOICE_TYPES, revertData } from './helpers.mjs';

const iface = new Interface(artifact.abi);
const gas = {};
let opened = 0;
let closed = 0;

async function setup(t) {
  const f = await fixture();
  opened++;
  t.after(async () => { await f.close(); closed++; });
  return f;
}

async function rejectsWith(operation, errorName) {
  await assert.rejects(operation, error => {
    assert.equal(revertData(error).slice(0, 10), iface.getError(errorName).selector, `${errorName}: ${error.shortMessage ?? error.message}`);
    return true;
  });
}

async function rawBalance(f, address) {
  return BigInt(await f.raw.request({ method: 'eth_getBalance', params: [address, 'latest'] }));
}

test('happy path: exact merchant credit, one event, paid flag, zero checkout balance', async t => {
  const f = await setup(t);
  const invoice = await f.invoice();
  const before = await rawBalance(f, invoice.merchant);
  const receipt = await f.pay(invoice);
  gas.payment = receipt.gasUsed.toString();
  assert.equal(receipt.status, 1);
  assert.equal(await rawBalance(f, invoice.merchant), before + invoice.amountWei);
  assert.equal(await rawBalance(f, await f.checkout.getAddress()), 0n);
  assert.equal(await f.checkout.paidInvoices(invoice.invoiceId), true);
  const events = receipt.logs.map(log => iface.parseLog(log)).filter(log => log?.name === 'CheckoutPaid');
  assert.equal(events.length, 1);
  for (const key of ['invoiceId', 'payer', 'merchant', 'amountWei', 'tenantHash']) assert.equal(events[0].args[key], invoice[key]);
});

test('ethers EIP-712 hash matches Solidity and IERC5267 domain', async t => {
  const f = await setup(t);
  const invoice = await f.invoice();
  assert.equal(await f.checkout.hashInvoice(invoice), TypedDataEncoder.hash(f.domain, INVOICE_TYPES, invoice));
  const domain = await f.checkout.eip712Domain();
  assert.equal(domain.name, 'TapeCommerceCheckout');
  assert.equal(domain.version, '1');
  assert.equal(domain.chainId, 196n);
  assert.equal(domain.verifyingContract, await f.checkout.getAddress());
  assert.equal(await f.checkout.INVOICE_TYPEHASH(), id('Invoice(bytes32 invoiceId,bytes32 tenantHash,address payer,address merchant,uint256 amountWei,uint64 deadline)'));
});

test('invalid signature signer is rejected', async t => {
  const f = await setup(t);
  const invoice = await f.invoice();
  const signature = await f.sign(invoice, f.domain, f.nextQuoteSigner);
  await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), 'InvalidSignature');
});

for (const signature of ['0x', '0x1234', '0x' + '00'.repeat(65)]) {
  test(`malformed signature (${signature.length - 2} hex characters) is rejected`, async t => {
    const f = await setup(t);
    const invoice = await f.invoice();
    await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), 'InvalidSignature');
  });
}

test('ECDSA high-s malleable signature is rejected', async t => {
  const f = await setup(t);
  const invoice = await f.invoice();
  const signature = await f.sign(invoice);
  const r = '0x' + signature.slice(2, 66);
  const s = BigInt('0x' + signature.slice(66, 130));
  const curveOrder = 0xfffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141n;
  const v = parseInt(signature.slice(130, 132), 16);
  const malleable = concat([r, toBeHex(curveOrder - s, 32), toBeHex(v === 27 ? 28 : 27, 1)]);
  await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, malleable, { value: invoice.amountWei }), 'InvalidSignature');
});

test('payer must be actual msg.sender', async t => {
  const f = await setup(t);
  const invoice = await f.invoice();
  const signature = await f.sign(invoice);
  await rejectsWith(() => f.checkout.connect(f.other).payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), 'WrongPayer');
});

for (const field of ['invoiceId', 'tenantHash', 'merchant', 'amountWei', 'deadline']) {
  test(`signed ${field} cannot be changed`, async t => {
    const f = await setup(t);
    const invoice = await f.invoice();
    const signature = await f.sign(invoice);
    const changes = {
      invoiceId: id('changed-invoice'), tenantHash: id('changed-tenant'), merchant: await f.other.getAddress(),
      amountWei: invoice.amountWei + 1n, deadline: invoice.deadline + 1n,
    };
    const altered = { ...invoice, [field]: changes[field] };
    await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(altered, signature, { value: altered.amountWei }), 'InvalidSignature');
  });
}

test('changed payer with matching caller still fails the original signature', async t => {
  const f = await setup(t);
  const invoice = await f.invoice();
  const signature = await f.sign(invoice);
  await rejectsWith(async () => f.checkout.connect(f.other).payOKB.staticCall({ ...invoice, payer: await f.other.getAddress() }, signature, { value: invoice.amountWei }), 'InvalidSignature');
});

for (const difference of [-1n, 1n]) {
  test(`exact value: ${difference < 0 ? 'underpayment' : 'overpayment'} rejected`, async t => {
    const f = await setup(t);
    const invoice = await f.invoice();
    const signature = await f.sign(invoice);
    await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, signature, { value: invoice.amountWei + difference }), 'IncorrectValue');
    assert.equal(await f.checkout.paidInvoices(invoice.invoiceId), false);
  });
}

test('expired invoice is rejected', async t => {
  const f = await setup(t);
  const invoice = await f.invoice({ deadline: 1n });
  const signature = await f.sign(invoice);
  await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), 'InvoiceExpired');
});

test('deadline is inclusive at exact block timestamp', async t => {
  const f = await setup(t);
  const block = await f.provider.getBlock('latest');
  const invoice = await f.invoice({ deadline: BigInt(block.timestamp) });
  await f.checkout.connect(f.payer).payOKB.staticCall(invoice, await f.sign(invoice), { value: invoice.amountWei });
});

test('invoice replay and reuse with a newly signed different tenant are rejected', async t => {
  const f = await setup(t);
  const invoice = await f.invoice();
  await f.pay(invoice);
  const signature = await f.sign(invoice);
  await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), 'InvoiceAlreadyPaid');
  const otherTenant = { ...invoice, tenantHash: id('another-tenant') };
  const otherSignature = await f.sign(otherTenant);
  await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(otherTenant, otherSignature, { value: invoice.amountWei }), 'InvoiceAlreadyPaid');
});

for (const [field, replacement] of [['chainId', 56], ['name', 'OtherCheckout'], ['version', '2'], ['verifyingContract', '0x0000000000000000000000000000000000000001']]) {
  test(`EIP-712 domain ${field} mismatch is rejected`, async t => {
    const f = await setup(t);
    const invoice = await f.invoice();
    const signature = await f.sign(invoice, { ...f.domain, [field]: replacement });
    await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), 'InvalidSignature');
  });
}

test('same-chain second contract cannot accept first contract quote', async t => {
  const f = await setup(t);
  const other = await f.deploy('TapeCommerceCheckout', [await f.owner.getAddress(), f.quoteSigner.address]);
  const invoice = await f.invoice();
  const signature = await f.sign(invoice);
  await rejectsWith(() => other.connect(f.payer).payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), 'InvalidSignature');
});

test('deployment on wrong chain is rejected', async t => {
  const f = await environment({ chainId: 56 });
  opened++;
  t.after(async () => { await f.close(); closed++; });
  await rejectsWith(async () => f.deploy('TapeCommerceCheckout', [await f.owner.getAddress(), f.quoteSigner.address]), 'WrongChain');
});

test('runtime network check rejects payment even if code is copied onto chain 56', async t => {
  const f = await setup(t);
  const wrongChain = await environment({ chainId: 56 });
  opened++;
  t.after(async () => { await wrongChain.close(); closed++; });
  const address = await f.checkout.getAddress();
  const code = await f.provider.getCode(address);
  await wrongChain.raw.request({ method: 'evm_setAccountCode', params: [address, code] });
  const copied = new Contract(address, artifact.abi, wrongChain.payer);
  const invoice = await f.invoice();
  const signature = await f.sign(invoice);
  await rejectsWith(() => copied.payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), 'WrongChain');
});

for (const [label, overrides, errorName] of [
  ['zero merchant', { merchant: ZeroAddress }, 'InvalidMerchant'],
  ['zero invoice', { invoiceId: ZeroHash }, 'InvalidInvoiceId'],
  ['zero tenant', { tenantHash: ZeroHash }, 'InvalidTenantHash'],
  ['zero amount', { amountWei: 0n }, 'InvalidAmount'],
]) {
  test(`${label} rejected even with valid signature`, async t => {
    const f = await setup(t);
    const invoice = await f.invoice(overrides);
    const signature = await f.sign(invoice);
    await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), errorName);
  });
}

test('checkout cannot be merchant', async t => {
  const f = await setup(t);
  const invoice = await f.invoice({ merchant: await f.checkout.getAddress() });
  const signature = await f.sign(invoice);
  await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), 'InvalidMerchant');
});

test('rejected merchant transfer rolls back flag, event and payer value; same invoice can retry', async t => {
  const f = await setup(t);
  const merchant = await f.deploy('RejectingMerchant');
  const invoice = await f.invoice({ merchant: await merchant.getAddress() });
  const signature = await f.sign(invoice);
  await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), 'MerchantTransferFailed');
  const before = await rawBalance(f, invoice.payer);
  const tx = await f.checkout.connect(f.payer).payOKB(invoice, signature, { value: invoice.amountWei, gasLimit: 300_000 });
  let receipt;
  try { await tx.wait(); assert.fail('expected on-chain revert'); } catch (error) { receipt = error.receipt; }
  assert.equal(receipt.status, 0);
  assert.equal(receipt.logs.length, 0);
  assert.equal(before - await rawBalance(f, invoice.payer), receipt.fee);
  assert.equal(await rawBalance(f, await f.checkout.getAddress()), 0n);
  assert.equal(await rawBalance(f, invoice.merchant), 0n);
  assert.equal(await f.checkout.paidInvoices(invoice.invoiceId), false);
  await (await merchant.setReject(false)).wait();
  await f.pay(invoice, signature);
  assert.equal(await rawBalance(f, invoice.merchant), invoice.amountWei);
});

test('malicious merchant nested payment is blocked while accepting outer payment', async t => {
  const f = await setup(t);
  const merchant = await f.deploy('ReentrantMerchant', [await f.checkout.getAddress()]);
  const address = await merchant.getAddress();
  const outer = await f.invoice({ merchant: address });
  const nested = await f.invoice({ invoiceId: id('nested'), payer: address, merchant: await f.other.getAddress() });
  const data = iface.encodeFunctionData('payOKB', [nested, await f.sign(nested)]);
  await (await merchant.configure(data, nested.amountWei, false)).wait();
  await f.pay(outer);
  assert.equal(await merchant.attempted(), true);
  assert.equal(await merchant.nestedSucceeded(), false);
  assert.equal(await merchant.nestedError(), id('ReentrancyGuardReentrantCall()').slice(0, 10));
  assert.equal(await f.checkout.paidInvoices(nested.invoiceId), false);
  assert.equal(await f.checkout.paidInvoices(outer.invoiceId), true);
  assert.equal(await rawBalance(f, address), outer.amountWei);
});

test('propagated reentrancy failure refunds outer payment and clears paid flag', async t => {
  const f = await setup(t);
  const merchant = await f.deploy('ReentrantMerchant', [await f.checkout.getAddress()]);
  const invoice = await f.invoice({ merchant: await merchant.getAddress() });
  const nested = await f.invoice({ invoiceId: id('nested-revert'), payer: await merchant.getAddress() });
  await (await merchant.configure(iface.encodeFunctionData('payOKB', [nested, await f.sign(nested)]), nested.amountWei, true)).wait();
  const signature = await f.sign(invoice);
  await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, signature, { value: invoice.amountWei }), 'MerchantTransferFailed');
  assert.equal(await f.checkout.paidInvoices(invoice.invoiceId), false);
  assert.equal(await rawBalance(f, invoice.merchant), 0n);
});

test('signer rotation is owner-only, emits event, revokes old quotes and accepts new ones', async t => {
  const f = await setup(t);
  const invoice = await f.invoice();
  const oldSignature = await f.sign(invoice);
  await rejectsWith(() => f.checkout.connect(f.other).setQuoteSigner.staticCall(f.nextQuoteSigner.address), 'OwnableUnauthorizedAccount');
  const receipt = await (await f.checkout.setQuoteSigner(f.nextQuoteSigner.address)).wait();
  const event = receipt.logs.map(log => iface.parseLog(log)).find(log => log.name === 'QuoteSignerChanged');
  assert.equal(event.args.previousSigner, f.quoteSigner.address);
  assert.equal(event.args.newSigner, f.nextQuoteSigner.address);
  assert.equal(await f.checkout.quoteSigner(), f.nextQuoteSigner.address);
  await rejectsWith(() => f.checkout.connect(f.payer).payOKB.staticCall(invoice, oldSignature, { value: invoice.amountWei }), 'InvalidSignature');
  await f.pay(invoice, await f.sign(invoice, f.domain, f.nextQuoteSigner));
});

test('zero signer rejected during deployment and rotation', async t => {
  const f = await setup(t);
  await rejectsWith(() => f.checkout.setQuoteSigner.staticCall(ZeroAddress), 'InvalidQuoteSigner');
  await rejectsWith(async () => f.deploy('TapeCommerceCheckout', [await f.owner.getAddress(), ZeroAddress]), 'InvalidQuoteSigner');
});

test('zero owner rejected', async t => {
  const f = await setup(t);
  await rejectsWith(() => f.deploy('TapeCommerceCheckout', [ZeroAddress, f.quoteSigner.address]), 'OwnableInvalidOwner');
});

test('ownership transfer needs acceptance and old owner loses rotation power', async t => {
  const f = await setup(t);
  const newOwner = await f.newOwner.getAddress();
  await (await f.checkout.transferOwnership(newOwner)).wait();
  assert.equal(await f.checkout.owner(), await f.owner.getAddress());
  assert.equal(await f.checkout.pendingOwner(), newOwner);
  await rejectsWith(() => f.checkout.connect(f.other).acceptOwnership.staticCall(), 'OwnableUnauthorizedAccount');
  await (await f.checkout.connect(f.newOwner).acceptOwnership()).wait();
  assert.equal(await f.checkout.owner(), newOwner);
  await rejectsWith(() => f.checkout.setQuoteSigner.staticCall(f.nextQuoteSigner.address), 'OwnableUnauthorizedAccount');
  await (await f.checkout.connect(f.newOwner).setQuoteSigner(f.nextQuoteSigner.address)).wait();
});

test('direct transfers and unknown payable selectors cannot trap accidental deposits', async t => {
  const f = await setup(t);
  const address = await f.checkout.getAddress();
  await rejectsWith(async () => f.provider.call({ from: await f.payer.getAddress(), to: address, value: 1n }), 'DirectPaymentUnsupported');
  await assert.rejects(f.provider.call({ from: await f.payer.getAddress(), to: address, value: 1n, data: '0xdeadbeef' }));
  assert.equal(await rawBalance(f, address), 0n);
});

test('forced EVM credit is isolated, cannot be withdrawn, and is never paid to next merchant', async t => {
  const f = await setup(t);
  const address = await f.checkout.getAddress();
  const forced = 123456n;
  await f.deploy('ForceCredit', [address], { value: forced });
  assert.equal(await rawBalance(f, address), forced);
  assert.equal(iface.fragments.filter(fragment => fragment.type === 'function' && /withdraw|sweep|refund|upgrade/i.test(fragment.name)).length, 0);
  const invoice = await f.invoice();
  const before = await rawBalance(f, invoice.merchant);
  await f.pay(invoice);
  assert.equal(await rawBalance(f, invoice.merchant), before + invoice.amountWei);
  assert.equal(await rawBalance(f, address), forced);
});

test('integer edge amounts and distinct invoice ids transfer exactly', async t => {
  const f = await setup(t);
  const amounts = [1n, 2n, 999n, 10n ** 9n, 9007199254740993n, 10n ** 18n];
  let total = 0n;
  const merchant = await f.merchant.getAddress();
  const before = await rawBalance(f, merchant);
  for (const [index, amountWei] of amounts.entries()) {
    const invoice = await f.invoice({ invoiceId: id(`edge-${index}`), amountWei });
    await f.pay(invoice);
    total += amountWei;
  }
  assert.equal(await rawBalance(f, merchant), before + total);
  assert.equal(await rawBalance(f, await f.checkout.getAddress()), 0n);
});

test('deployment gas is measured locally, not advertised as a current mainnet quote', async t => {
  const f = await setup(t);
  const receipt = await f.checkout.deploymentTransaction().wait();
  gas.deployment = receipt.gasUsed.toString();
  assert.ok(receipt.gasUsed > 0n);
});

after(() => {
  assert.equal(opened, closed, 'every in-memory EVM must disconnect');
  fs.mkdirSync(path.join(ROOT, 'evidence'), { recursive: true });
  fs.writeFileSync(path.join(ROOT, 'evidence/local-gas.json'), JSON.stringify({
    environment: 'Ganache 7.9.2 in-memory EVM, chainId 196, shanghai; solc 0.8.30 evmVersion paris optimizer200',
    gasUnits: gas,
    providersOpened: opened,
    providersDisconnected: closed,
    mainnetDeployed: false,
  }, null, 2) + '\n');
});
