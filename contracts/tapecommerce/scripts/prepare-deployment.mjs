// SPDX-License-Identifier: GPL-3.0-only
// Creates an unsigned transaction only. No private key, wallet, or send method.
import fs from 'node:fs';
import path from 'node:path';
import { ContractFactory, getAddress, ZeroAddress, AbiCoder } from 'ethers';
import { ROOT, compile } from './compile.mjs';

function argument(name) {
  const i = process.argv.indexOf(name);
  return i === -1 ? undefined : process.argv[i + 1];
}

const ownerInput = argument('--owner');
const quoteSignerInput = argument('--quote-signer');
const deployerInput = argument('--deployer');
if (!ownerInput || !quoteSignerInput || !deployerInput) {
  console.error('Usage: npm run prepare:deployment -- --owner <multisig-address> --quote-signer <public-signing-address> --deployer <wallet-address> [--rpc <read-only-rpc-url>] [--offline]');
  console.error('No keys are accepted and nothing is broadcast. Supply public addresses after approval of the deployment design.');
  process.exitCode = 1;
} else {
  const owner = getAddress(ownerInput);
  const quoteSigner = getAddress(quoteSignerInput);
  const deployer = getAddress(deployerInput);
  if ([owner, quoteSigner, deployer].includes(ZeroAddress)) throw new Error('Owner, quote signer and deployer must be nonzero');
  compile({ writeArtifacts: true });
  const artifact = JSON.parse(fs.readFileSync(path.join(ROOT, 'artifacts/TapeCommerceCheckout.json'), 'utf8'));
  const data = (await new ContractFactory(artifact.abi, artifact.bytecode).getDeployTransaction(owner, quoteSigner)).data;
  const output = {
    status: 'unsigned_preparation_only',
    chainId: 196,
    owner,
    quoteSigner,
    deployer,
    constructorArgumentsAbiEncoded: AbiCoder.defaultAbiCoder().encode(['address', 'address'], [owner, quoteSigner]),
    transaction: { from: deployer, data, value: '0x0', chainId: '0xc4' },
    deploymentAddress: null,
    gasEstimate: null,
    requiresApproval: ['owner and quote signer addresses', 'source/security review', 'current gas budget', 'wallet-signed mainnet deployment'],
    sent: false,
  };
  if (!process.argv.includes('--offline')) {
    const rpcUrl = argument('--rpc') ?? 'https://rpc.xlayer.tech';
    const read = async (method, params) => {
      if (!['eth_chainId', 'eth_estimateGas', 'eth_gasPrice'].includes(method)) throw new Error('Read-only method allowlist');
      const response = await fetch(rpcUrl, {
        method: 'POST', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }), signal: AbortSignal.timeout(15_000),
      });
      if (!response.ok) throw new Error(`RPC HTTP ${response.status}`);
      const result = await response.json();
      if (result.error) throw new Error(`${method}: ${JSON.stringify(result.error)}`);
      return result.result;
    };
    if (BigInt(await read('eth_chainId', [])) !== 196n) throw new Error('RPC is not X Layer chainId196');
    const gas = BigInt(await read('eth_estimateGas', [{ from: deployer, data, value: '0x0' }]));
    const price = BigInt(await read('eth_gasPrice', []));
    output.gasEstimate = { units: gas.toString(), gasPriceWei: price.toString(), estimatedFeeWei: (gas * price).toString(), observedAt: new Date().toISOString() };
  }
  const destination = path.join(ROOT, 'deployment-preparation.json');
  fs.writeFileSync(destination, JSON.stringify(output, null, 2) + '\n');
  console.log(`Unsigned preparation written to ${destination}; no transaction sent.`);
}
