// SPDX-License-Identifier: GPL-3.0-only
import test from 'node:test';
import assert from 'node:assert/strict';
import { ABI, NETWORK, EXPECTED_IMPLEMENTATIONS, createReadonlyRpc, readIdentity } from './readonly-adapter.mjs';

const processor = '0x1111111111111111111111111111111111111111';
const container = '0x2222222222222222222222222222222222222222';
const holder = '0x3333333333333333333333333333333333333333';
const block = { number: '0x64', hash: `0x${'a'.repeat(64)}`, timestamp: '0x6553f100' };
function fixture(overrides = {}) {
  const calls = [];
  const rpc = async (method, params = []) => {
    calls.push({ method, params });
    const override = overrides[method];
    if (override) return override(params, calls);
    if (method === 'eth_chainId') return '0xc4';
    if (method === 'eth_getBlockByNumber') return block;
    if (method === 'eth_getCode') return '0x6000';
    if (method === 'eth_getStorageAt') {
      const name = Object.entries(NETWORK).find(([, address]) => address === params[0])?.[0];
      return `0x${(EXPECTED_IMPLEMENTATIONS[name] ?? '0x0').slice(2).padStart(64, '0')}`;
    }
    if (method === 'eth_call') {
      const decoded = ABI.parseTransaction({ data: params[0].data });
      const values = { cpuCount: [3n], cpuAt: [processor], ownerOf: [holder], accountOf: [container], isOpened: [true], token: [196n, processor, 1n], isLive: [false], isContainerLive: [true], containerPaidUntil: [1999999999n] };
      return ABI.encodeFunctionResult(decoded.name, values[decoded.name]);
    }
    throw new Error(`Unexpected method ${method}`);
  };
  return { rpc, calls };
}

test('resolves factory identity, pins all state reads and rechecks block hash', async () => {
  const { rpc, calls } = fixture();
  const result = await readIdentity({ processorNumber: '2', tokenId: '1', expectedOwner: holder, rpc });
  assert.equal(result.container, container);
  assert.equal(result.live, true);
  assert.equal(result.name, '1.2.2.tape');
  assert.equal(result.blockNumber, 100);
  assert.equal(result.finality, 'safe');
  for (const call of calls.filter((c) => ['eth_call', 'eth_getCode', 'eth_getStorageAt'].includes(c.method))) assert.equal(call.params.at(-1), '0x64');
  assert.deepEqual(calls.at(-1), { method: 'eth_getBlockByNumber', params: ['0x64', false] });
});

test('fails closed on a wrong chain before reading contracts', async () => {
  const f = fixture({ eth_chainId: () => '0x38' });
  await assert.rejects(readIdentity({ processorNumber: '2', tokenId: '1', rpc: f.rpc }), /Wrong chain/);
  assert.equal(f.calls.length, 1);
});

test('fails closed when safe is unavailable, never falls back to latest', async () => {
  const f = fixture({ eth_getBlockByNumber: () => null });
  await assert.rejects(readIdentity({ processorNumber: 2, tokenId: 1, rpc: f.rpc }), /safe block/);
  assert.ok(f.calls.every((c) => !c.params.includes('latest')));
});

test('rejects missing protocol bytecode', async () => {
  const f = fixture({ eth_getCode: () => '0x' });
  await assert.rejects(readIdentity({ processorNumber: 2, tokenId: 1, rpc: f.rpc }), /missing bytecode/);
});

test('rejects unreviewed implementation upgrades', async () => {
  const f = fixture({ eth_getStorageAt: () => `0x${'0'.repeat(63)}1` });
  await assert.rejects(readIdentity({ processorNumber: 2, tokenId: 1, rpc: f.rpc }), /implementation changed/);
});

test('rejects out-of-range processor rather than accepting arbitrary address', async () => {
  await assert.rejects(readIdentity({ processorNumber: 344, tokenId: 1, rpc: fixture().rpc }), /Processor does not exist/);
});

test('rejects wallet that is no longer NFT holder', async () => {
  await assert.rejects(readIdentity({ processorNumber: 2, tokenId: 1, expectedOwner: container, rpc: fixture().rpc }), /holder does not match/);
});

test('rejects reorg or inconsistent pinned block', async () => {
  const f = fixture({ eth_getBlockByNumber: ([tag]) => tag === 'safe' ? block : { ...block, hash: `0x${'b'.repeat(64)}` } });
  await assert.rejects(readIdentity({ processorNumber: 2, tokenId: 1, rpc: f.rpc }), /Pinned safe block changed/);
});

test('rejects container whose ERC-6551 binding reports another chain', async () => {
  const base = fixture();
  const f = fixture({ eth_call: async (params) => ABI.parseTransaction({ data: params[0].data }).name === 'token'
    ? ABI.encodeFunctionResult('token', [56n, processor, 1n]) : base.rpc('eth_call', params) });
  await assert.rejects(readIdentity({ processorNumber: 2, tokenId: 1, rpc: f.rpc }), /Container token binding/);
});

test('validates uint256 inputs without precision loss or negative values', async () => {
  for (const invalid of ['-1', '2.5', '01', '1e3', 'foo', (2n ** 256n).toString()]) await assert.rejects(readIdentity({ processorNumber: invalid, tokenId: 1, rpc: fixture().rpc }), /unsigned|exceeds/);
});

test('read-only RPC refuses all signing, wallet and broadcast methods before fetch', async () => {
  let fetched = false;
  const rpc = createReadonlyRpc({ fetchImpl: () => { fetched = true; } });
  for (const method of ['eth_sendRawTransaction', 'eth_sendTransaction', 'eth_sign', 'personal_sign', 'eth_requestAccounts']) await assert.rejects(rpc(method), /forbidden/);
  assert.equal(fetched, false);
});

test('RPC HTTP/error response and mismatched id never become evidence', async () => {
  for (const body of [{ jsonrpc: '2.0', id: 1, error: { code: -32000, message: 'offline' } }, { jsonrpc: '2.0', id: 9, result: '0xc4' }]) {
    const rpc = createReadonlyRpc({ fetchImpl: async () => ({ ok: true, json: async () => body }) });
    await assert.rejects(rpc('eth_chainId'), /offline|envelope/);
  }
});
