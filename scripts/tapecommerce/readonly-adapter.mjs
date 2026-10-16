// SPDX-License-Identifier: GPL-3.0-only
import { createRequire } from 'node:module';

// Reuse the application's pinned ethers dependency. No wallet/signer is created.
const { Interface, getAddress, keccak256 } = createRequire(new URL('../../platform/package.json', import.meta.url))('ethers');

export const SOURCE_COMMIT = '40994bd65baedbda4df72cb0985031a5f082f606';
export const IMPL_SLOT = '0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc';
export const NETWORK = Object.freeze({
  chainId: 196, area: 2, currency: 'OKB', rpcUrl: 'https://rpc.xlayer.tech',
  factory: '0x1f09daefa827f02cbb40967cc91b259763760761',
  opener: '0x536add8f30f03b69f6fbf29d425a816a0dc50106',
  registry: '0xd6efb7adcc9c83dc4924ad56f6a8e4e969b9adb6',
  binding: '0x68809fd2fb343aa57d0aeb7f33defe477c9666f9',
});
export const EXPECTED_IMPLEMENTATIONS = Object.freeze({
  factory: '0x74956236ab64ed143933040b4137e8a352e4d17b',
  opener: null,
  registry: '0xa85c4143d1d4a77f54b8e4ecc9e6d1418afea45f',
  binding: '0x5ebf29b80789e548907c707530c3c7607c4347df',
});
export const READ_ABI = Object.freeze([
  'function cpuCount() view returns (uint256)',
  'function cpuAt(uint256) view returns (address)',
  'function isCPU(address) view returns (bool)',
  'function ownerOf(uint256) view returns (address)',
  'function accountOf(address,uint256) view returns (address)',
  'function isOpened(address,uint256) view returns (bool)',
  'function token() view returns (uint256,address,uint256)',
  'function FEE() view returns (uint256)',
  'function monthlyFee() view returns (uint256)',
  'function isContainerLive(address) view returns (bool)',
  'function containerPaidUntil(address) view returns (uint256)',
  'function isLive(string,address) view returns (bool)',
  'function pathCount(address) view returns (uint256)',
  'function pathsRange(address,uint256,uint256) view returns (string[])',
  'function fallbackPath(address) view returns (string)',
  'function fileInfo(address,string) view returns (uint32,string,bytes32,uint40,uint256)',
  'function read(address,string) view returns (bytes)',
]);
export const ABI = new Interface(READ_ABI);
const ALLOWED_METHODS = new Set(['eth_chainId', 'eth_getBlockByNumber', 'eth_blockNumber', 'eth_getCode', 'eth_getStorageAt', 'eth_call', 'eth_getLogs', 'eth_gasPrice', 'eth_estimateGas']);
const ZERO = '0x0000000000000000000000000000000000000000';
const safeUint = (value, label) => {
  if (!/^(0|[1-9][0-9]*)$/.test(String(value))) throw new Error(`${label} must be an unsigned decimal integer`);
  const n = BigInt(value);
  if (n >= 2n ** 256n) throw new Error(`${label} exceeds uint256`);
  return n;
};

export function createReadonlyRpc({ rpcUrl = NETWORK.rpcUrl, fetchImpl = globalThis.fetch, timeoutMs = 12_000 } = {}) {
  const url = new URL(rpcUrl);
  if (url.protocol !== 'https:' || url.username || url.password) throw new Error('Public HTTPS RPC required');
  let id = 0;
  return async (method, params = []) => {
    if (!ALLOWED_METHODS.has(method)) throw new Error(`RPC method forbidden: ${method}`);
    const requestId = ++id;
    const response = await fetchImpl(url.href, {
      method: 'POST', headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ jsonrpc: '2.0', id: requestId, method, params }),
      signal: AbortSignal.timeout(timeoutMs),
    });
    if (!response.ok) throw new Error(`RPC HTTP ${response.status}`);
    const body = await response.json();
    if (body.id !== requestId || body.jsonrpc !== '2.0') throw new Error('Invalid JSON-RPC response envelope');
    if (body.error) throw new Error(`RPC ${body.error.code}: ${body.error.message}`);
    if (!Object.hasOwn(body, 'result')) throw new Error('RPC missing result');
    return body.result;
  };
}

export async function readSafeBlock(rpc) {
  const chainId = await rpc('eth_chainId');
  if (BigInt(chainId) !== BigInt(NETWORK.chainId)) throw new Error(`Wrong chain: expected 196, received ${chainId}`);
  const block = await rpc('eth_getBlockByNumber', ['safe', false]);
  if (!block || !/^0x[0-9a-f]+$/i.test(block.number) || !/^0x[0-9a-f]{64}$/i.test(block.hash)) throw new Error('A valid safe block is required');
  if (BigInt(block.number) > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('Block number exceeds safe integer');
  return block;
}

export async function verifyPinnedBlock(rpc, block) {
  const again = await rpc('eth_getBlockByNumber', [block.number, false]);
  if (!again || again.hash?.toLowerCase() !== block.hash.toLowerCase()) throw new Error('Pinned safe block changed; retry later');
}

export async function readContract(rpc, address, name, args, blockTag) {
  const result = await rpc('eth_call', [{ to: getAddress(address), data: ABI.encodeFunctionData(name, args) }, blockTag]);
  return ABI.decodeFunctionResult(name, result);
}

export async function verifyContracts(rpc, blockTag) {
  const records = {};
  for (const name of ['factory', 'opener', 'registry', 'binding']) {
    const address = NETWORK[name];
    const code = await rpc('eth_getCode', [address, blockTag]);
    if (!/^0x(?:[0-9a-f]{2})+$/i.test(code)) throw new Error(`${name}: missing bytecode`);
    const storage = await rpc('eth_getStorageAt', [address, IMPL_SLOT, blockTag]);
    if (!/^0x[0-9a-f]{64}$/i.test(storage)) throw new Error(`${name}: invalid implementation slot`);
    const implementation = `0x${storage.slice(-40)}`.toLowerCase();
    const expected = EXPECTED_IMPLEMENTATIONS[name];
    if (implementation !== (expected ?? ZERO)) throw new Error(`${name}: implementation changed; expected ${expected ?? ZERO}, received ${implementation}`);
    let implementationCode = null;
    if (expected) {
      implementationCode = await rpc('eth_getCode', [implementation, blockTag]);
      if (!/^0x(?:[0-9a-f]{2})+$/i.test(implementationCode)) throw new Error(`${name}: implementation has no code`);
    }
    records[name] = { address, codeBytes: (code.length - 2) / 2, codeKeccak256: keccak256(code), implementation: expected ? implementation : null, implementationCodeBytes: implementationCode ? (implementationCode.length - 2) / 2 : 0, implementationCodeKeccak256: implementationCode ? keccak256(implementationCode) : null, matchesPinnedConfig: true };
  }
  return records;
}

/** Resolves one identity from trusted factory state at one safe block.
 * A snapshot, not a persistent authorization: re-run before every quote/publish.
 */
export async function readIdentity({ processorNumber, tokenId, rpcUrl, fetchImpl, timeoutMs, rpc: suppliedRpc, expectedOwner } = {}) {
  const cpu = safeUint(processorNumber, 'processorNumber');
  const id = safeUint(tokenId, 'tokenId');
  const rpc = suppliedRpc ?? createReadonlyRpc({ rpcUrl, fetchImpl, timeoutMs });
  const block = await readSafeBlock(rpc);
  await verifyContracts(rpc, block.number);
  const call = (to, name, args = []) => readContract(rpc, to, name, args, block.number);
  const [count] = await call(NETWORK.factory, 'cpuCount');
  if (cpu >= count) throw new Error(`Processor does not exist: ${cpu}; cpuCount=${count}`);
  const [processor] = await call(NETWORK.factory, 'cpuAt', [cpu]);
  if (processor.toLowerCase() === ZERO) throw new Error('Factory returned zero processor');
  const [[holder], [container], [opened]] = await Promise.all([
    call(processor, 'ownerOf', [id]), call(NETWORK.opener, 'accountOf', [processor, id]), call(NETWORK.opener, 'isOpened', [processor, id]),
  ]);
  if (holder.toLowerCase() === ZERO || container.toLowerCase() === ZERO) throw new Error('Invalid holder/container');
  if (expectedOwner && getAddress(expectedOwner).toLowerCase() !== holder.toLowerCase()) throw new Error('Current NFT holder does not match expected wallet');
  if (opened) {
    const containerCode = await rpc('eth_getCode', [container, block.number]);
    if (!/^0x(?:[0-9a-f]{2})+$/i.test(containerCode)) throw new Error('Opened container has no bytecode');
    const [containerChain, containerProcessor, containerToken] = await call(container, 'token');
    if (containerChain !== 196n || containerProcessor.toLowerCase() !== processor.toLowerCase() || containerToken !== id) throw new Error('Container token binding does not match factory identity');
  }
  const name = `${id}.2.${cpu}.tape`;
  const [[nameLive], [containerLive], [paidUntil]] = await Promise.all([
    call(NETWORK.binding, 'isLive', [name, container]), call(NETWORK.binding, 'isContainerLive', [container]), call(NETWORK.binding, 'containerPaidUntil', [container]),
  ]);
  await verifyPinnedBlock(rpc, block);
  return { chainId: 196, processorNumber: cpu.toString(), tokenId: id.toString(), holder, processor, container, opened, live: opened && (nameLive || containerLive), nameLive, containerLive, paidUntil: paidUntil.toString(), name, gatewayUrl: `https://${id}-2-${cpu}.tapekit.org/`, blockNumber: Number(BigInt(block.number)), blockHash: block.hash, blockTimestamp: Number(BigInt(block.timestamp)), finality: 'safe' };
}
