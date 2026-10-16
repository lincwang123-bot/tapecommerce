// SPDX-License-Identifier: GPL-3.0-only
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { createRequire } from 'node:module';
import { mkdtemp, mkdir, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomBytes } from 'node:crypto';
const require = createRequire(new URL('../platform/package.json', import.meta.url));
const { JsonRpcProvider, Wallet, ContractFactory, Contract, keccak256, toUtf8Bytes, toBeHex } = require('ethers');
const here = path.dirname(fileURLToPath(import.meta.url));
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const fail = (code, status = 503) => { throw Object.assign(new Error(code), { code, status }); };
const TENANT = 'tapecommerce-isolated-demo';

async function listen(server) {
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  return server.address().port;
}
async function stopServer(server) {
  server.closeAllConnections();
  await new Promise(resolve => server.close(resolve));
}
async function stopChild(child) {
  if (!child.pid) return; // Failed spawn emits error/close, not exit.
  if (child.exitCode !== null || child.signalCode !== null) return;
  const closed = new Promise(resolve => child.once('close', resolve));
  child.kill('SIGTERM');
  const timer = setTimeout(() => child.kill('SIGKILL'), 4000);
  await closed;
  clearTimeout(timer);
}

// This runtime creates its own private EVM and disposable database. It cannot
// connect a wallet, accept a mainnet RPC, or submit a transaction to X Layer.
export async function startSandbox({ onFailure = () => {}, onProcess = () => {} } = {}) {
  const children = [], relays = [];
  let closed = false, provider, directory;
  const close = async () => {
    if (closed) return;
    closed = true;
    provider?.destroy();
    await Promise.allSettled(relays.map(stopServer));
    await Promise.allSettled(children.map(stopChild));
    if (directory) await rm(directory, { recursive: true, force: true });
  };
  const child = (command, args, env) => {
    const proc = spawn(command, args, { env, stdio: ['ignore', 'pipe', 'pipe'] });
    children.push(proc);
    if (proc.pid) onProcess(proc.pid);
    proc.once('error', () => onFailure());
    proc.once('exit', () => { if (!closed) onFailure(); });
    // Never relay child environment, private keys, or raw database errors.
    proc.stderr.resume();
    return proc;
  };
  try {
    const root = process.env.TAPECOMMERCE_DATA_DIR || tmpdir();
    await mkdir(root, { recursive: true, mode: 0o700 });
    directory = await mkdtemp(path.join(root, 'tape-demo-'));
    const reservation = createServer();
    const port = await listen(reservation);
    await stopServer(reservation);
    const architecture = process.arch === 'x64' ? 'amd64' : process.arch;
    const binary = process.env.ANVIL_BIN || path.join(here, 'node_modules', '@foundry-rs', `anvil-${process.platform}-${architecture}`, 'bin', 'anvil');
    const anvil = child(binary, ['--host', '127.0.0.1', '--port', String(port), '--chain-id', '196', '--accounts', '0', '--silent', '--threads', '1', '--hardfork', 'paris'], process.env);
    anvil.stdout.resume();
    const endpoint = `http://127.0.0.1:${port}`;
    let requestId = 0;
    const rpc = async (method, params = []) => {
      const response = await fetch(endpoint, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ jsonrpc: '2.0', id: ++requestId, method, params }), signal: AbortSignal.timeout(5000) });
      const result = await response.json();
      if (!response.ok || result.error) fail('sandbox_rpc_unavailable');
      return result.result;
    };
    let started = false;
    for (let n = 0; n < 60; n++) {
      if (anvil.exitCode !== null) fail('sandbox_start_failed');
      try { if (/anvil/i.test(await rpc('web3_clientVersion'))) { started = true; break; } } catch { /* Wait for the owned process. */ }
      await sleep(100);
    }
    if (!started || await rpc('eth_chainId') !== '0xc4') fail('sandbox_start_failed');
    provider = new JsonRpcProvider(endpoint, 196, { staticNetwork: true, cacheTimeout: -1, pollingInterval: 100 });
    const owner = Wallet.createRandom().connect(provider), signer = Wallet.createRandom(), merchant = Wallet.createRandom();
    await rpc('anvil_setBalance', [owner.address, toBeHex(100n * 10n ** 18n)]);
    const artifact = JSON.parse(await readFile(new URL('./assets/checkout.json', import.meta.url), 'utf8'));
    const bounded = async (work, milliseconds) => {
      let timer;
      try { return await Promise.race([work, new Promise((_, reject) => { timer = setTimeout(() => { onFailure(); void close(); reject(Object.assign(new Error('sandbox_timeout'), { code: 'sandbox_timeout', status: 503 })); }, milliseconds); })]); }
      finally { clearTimeout(timer); }
    };
    const contract = await bounded(new ContractFactory(artifact.abi, artifact.bytecode, owner).deploy(owner.address, signer.address), 15000);
    await bounded(contract.waitForDeployment(), 15000);
    const checkout = await contract.getAddress();
    // Two transport paths exercise the verifier contract, but share one local
    // simulator. They are deliberately NOT advertised as independent RPC trust.
    for (let n = 0; n < 2; n++) {
      const relay = createServer(async (req, res) => {
        try {
          if (req.method !== 'POST') throw new Error();
          let body = ''; for await (const chunk of req) { body += chunk; if (body.length > 32768) throw new Error(); }
          const message = JSON.parse(body);
          if (!['eth_chainId', 'eth_getTransactionReceipt', 'eth_getBlockByNumber', 'eth_blockNumber'].includes(message.method) || !Array.isArray(message.params)) throw new Error();
          const result = await rpc(message.method, message.params);
          res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify({ jsonrpc: '2.0', id: message.id, result }));
        } catch { res.writeHead(400).end(); }
      });
      await listen(relay); relays.push(relay);
    }
    const secret = randomBytes(32).toString('hex');
    const bridgeBin = process.env.TAPECOMMERCE_BRIDGE_BIN || path.join(here, 'bin', 'tapecommerce-demo');
    const bridge = child(bridgeBin, [], { ...process.env,
      TAPE_DEMO_CHECKOUT: checkout, TAPE_DEMO_MERCHANT: merchant.address, TAPE_DEMO_TENANT_HASH: keccak256(toUtf8Bytes(TENANT)),
      TAPE_DEMO_RPC_PRIMARY: `http://127.0.0.1:${relays[0].address().port}`,
      TAPE_DEMO_RPC_SECONDARY: `http://127.0.0.1:${relays[1].address().port}`,
      TAPE_DEMO_SECRET: secret, TAPE_DEMO_TMP_ROOT: directory, TAPE_DEMO_PORT: '0',
    });
    const bridgePort = await new Promise((resolve, reject) => {
      let buffer = '';
      const timer = setTimeout(() => reject(new Error('demo_bridge_timeout')), 20000);
      const finish = value => { clearTimeout(timer); resolve(value); };
      bridge.once('error', () => { clearTimeout(timer); reject(new Error('demo_bridge_missing')); });
      bridge.once('exit', () => { clearTimeout(timer); reject(new Error('demo_bridge_failed')); });
      bridge.stdout.on('data', data => {
        buffer += data;
        if (buffer.length > 65536) { clearTimeout(timer); reject(new Error('demo_bridge_protocol')); return; }
        const lines = buffer.split('\n'); buffer = lines.pop();
        for (const line of lines) { try { const v = JSON.parse(line); if (Number.isInteger(v.port) && v.port > 0 && v.port < 65536) finish(v.port); } catch { /* Discard non-protocol logs. */ } }
      });
    });
    const call = async (route, data) => {
      if (closed) fail('demo_unavailable');
      const response = await fetch(`http://127.0.0.1:${bridgePort}${route}`, { method: data === undefined ? 'GET' : 'POST', headers: { authorization: `Bearer ${secret}`, 'content-type': 'application/json' }, ...(data === undefined ? {} : { body: JSON.stringify(data) }), signal: AbortSignal.timeout(15000) });
      const payload = await response.json();
      if (!response.ok) fail(typeof payload.error === 'string' ? payload.error : 'demo_order_failed', response.status);
      return payload;
    };
    let queue = Promise.resolve(), queued = 0;
    return { close, call, signer, merchant: merchant.address, checkout, tenant: TENANT,
      pids: children.map(proc => proc.pid), sourceMode: 'isolated_evm',
      async payer() {
        const wallet = Wallet.createRandom().connect(provider);
        await rpc('anvil_setBalance', [wallet.address, toBeHex(100n * 10n ** 18n)]);
        return wallet;
      },
      pay(wallet, quote) {
        if (queued >= 8 || closed) fail('demo_busy');
        queued++;
        const job = queue.then(async () => {
          if (closed) fail('demo_unavailable');
          // Use wall time because an idle private chain otherwise retains the
          // timestamp of its last block and cannot verify quote issuance time.
          const latest = await rpc('eth_getBlockByNumber', ['latest', false]);
          await rpc('evm_setNextBlockTimestamp', [Math.max(Math.floor(Date.now() / 1000), Number(BigInt(latest.timestamp)) + 1)]);
          const tx = await bounded(new Contract(checkout, artifact.abi, wallet).payOKB(quote.invoice, quote.signature, { value: quote.invoice.amountWei }), 15000);
          const receipt = await bounded(tx.wait(), 15000);
          await rpc('anvil_mine', ['0x40', '0x0']);
          return { txHash: tx.hash, blockNumber: receipt.blockNumber, blockHash: receipt.blockHash };
        }).finally(() => { queued--; });
        queue = job.catch(() => {});
        return job;
      },
    };
  } catch (error) { await close(); throw error; }
}
