// SPDX-License-Identifier: GPL-3.0-only
import ganache from 'ganache';
import { BrowserProvider, ContractFactory, Wallet, id, ZeroHash } from 'ethers';
import { compile } from '../scripts/compile.mjs';

export const INVOICE_TYPES = {
  Invoice: [
    { name: 'invoiceId', type: 'bytes32' },
    { name: 'tenantHash', type: 'bytes32' },
    { name: 'payer', type: 'address' },
    { name: 'merchant', type: 'address' },
    { name: 'amountWei', type: 'uint256' },
    { name: 'deadline', type: 'uint64' },
  ],
};

export const compiled = compile({ includeFixtures: true });
export const artifact = compiled['src/TapeCommerceCheckout.sol'].TapeCommerceCheckout;

export async function environment({ chainId = 196, fork } = {}) {
  const raw = ganache.provider({
    chain: { chainId, hardfork: 'shanghai' },
    wallet: { deterministic: true, totalAccounts: 6, defaultBalance: 1000 },
    miner: { blockGasLimit: 30_000_000 },
    logging: { quiet: true },
    ...(fork ? { fork } : {}),
  });
  const provider = new BrowserProvider(raw, undefined, { cacheTimeout: -1 });
  provider.pollingInterval = 10;
  let entries;
  let signers;
  try {
    entries = Object.entries(raw.getInitialAccounts());
    signers = await Promise.all(entries.map(([address]) => provider.getSigner(address)));
  } catch (error) {
    provider.destroy();
    await raw.disconnect();
    throw error;
  }
  const wallets = entries.map(([, data]) => new Wallet(data.secretKey));
  const [owner, payer, merchant, other, newOwner] = signers;
  const quoteSigner = wallets[5];
  const nextQuoteSigner = wallets[4];
  return {
    raw, provider, owner, payer, merchant, other, newOwner, wallets, quoteSigner, nextQuoteSigner,
    async close() { provider.destroy(); await raw.disconnect(); },
    async deploy(contractName, args = [], overrides = {}) {
      const item = contractName === 'TapeCommerceCheckout' ? artifact : compiled['test/AttackFixtures.sol'][contractName];
      const instance = await new ContractFactory(item.abi, item.evm.bytecode.object, owner).deploy(...args, overrides);
      await instance.waitForDeployment();
      return instance;
    },
  };
}

export async function fixture() {
  const env = await environment();
  try {
    env.checkout = await env.deploy('TapeCommerceCheckout', [await env.owner.getAddress(), env.quoteSigner.address]);
    env.domain = { name: 'TapeCommerceCheckout', version: '1', chainId: 196, verifyingContract: await env.checkout.getAddress() };
    env.invoice = async (overrides = {}) => ({
      invoiceId: id('local-test-invoice'), tenantHash: id('local-test-tenant'),
      payer: await env.payer.getAddress(), merchant: await env.merchant.getAddress(),
      amountWei: 10n ** 16n,
      deadline: BigInt((await env.provider.getBlock('latest')).timestamp + 300),
      ...overrides,
    });
    env.sign = (invoice, domain = env.domain, signer = env.quoteSigner) => signer.signTypedData(domain, INVOICE_TYPES, invoice);
    env.pay = async (invoice, signature) => {
      const tx = await env.checkout.connect(env.payer).payOKB(invoice, signature ?? await env.sign(invoice), { value: invoice.amountWei });
      return tx.wait();
    };
    return env;
  } catch (error) { await env.close(); throw error; }
}

export function revertData(error) {
  const candidates = [error.data, error.info?.error?.data?.result, error.info?.error?.data, error.error?.data];
  return candidates.find(value => typeof value === 'string' && value.startsWith('0x')) ?? ZeroHash;
}
