// SPDX-License-Identifier: GPL-3.0-only
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import solc from 'solc';

export const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export function compile({ includeFixtures = false, writeArtifacts = false } = {}) {
  const sourcePaths = ['src/TapeCommerceCheckout.sol'];
  if (includeFixtures) sourcePaths.push('test/AttackFixtures.sol');
  const sources = Object.fromEntries(sourcePaths.map(name => [name, { content: fs.readFileSync(path.join(ROOT, name), 'utf8') }]));
  const input = {
    language: 'Solidity',
    sources,
    settings: {
      optimizer: { enabled: true, runs: 200 },
      // Avoid requiring newer EVM opcodes before validating network support.
      evmVersion: 'paris',
      metadata: { bytecodeHash: 'ipfs' },
      outputSelection: { '*': { '*': ['abi', 'evm.bytecode.object', 'evm.deployedBytecode.object', 'metadata'] } },
    },
  };
  const resolvedSources = { ...sources };
  const output = JSON.parse(solc.compile(JSON.stringify(input), {
    import(importPath) {
      const absolute = path.resolve(ROOT, 'node_modules', importPath);
      if (!absolute.startsWith(path.join(ROOT, 'node_modules') + path.sep)) return { error: 'Import outside node_modules' };
      try {
        const content = fs.readFileSync(absolute, 'utf8');
        resolvedSources[importPath] = { content };
        return { contents: content };
      } catch (error) { return { error: error.message }; }
    },
  }));
  const errors = (output.errors ?? []).filter(error => error.severity === 'error');
  if (errors.length) throw new Error(errors.map(error => error.formattedMessage).join('\n'));
  const artifact = output.contracts['src/TapeCommerceCheckout.sol'].TapeCommerceCheckout;
  if (writeArtifacts) {
    const dir = path.join(ROOT, 'artifacts');
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, 'TapeCommerceCheckout.json'), JSON.stringify({
      contractName: 'TapeCommerceCheckout',
      sourceName: 'src/TapeCommerceCheckout.sol',
      compilerVersion: solc.version(),
      optimizer: input.settings.optimizer,
      evmVersion: input.settings.evmVersion,
      sourceSha256: createHash('sha256').update(sources['src/TapeCommerceCheckout.sol'].content).digest('hex'),
      abi: artifact.abi,
      bytecode: `0x${artifact.evm.bytecode.object}`,
      deployedBytecode: `0x${artifact.evm.deployedBytecode.object}`,
      metadata: JSON.parse(artifact.metadata),
    }, null, 2) + '\n');
    fs.writeFileSync(path.join(dir, 'TapeCommerceCheckout.abi.json'), JSON.stringify(artifact.abi, null, 2) + '\n');
    fs.writeFileSync(path.join(dir, 'standard-input.json'), JSON.stringify({ ...input, sources: resolvedSources }, null, 2) + '\n');
    fs.writeFileSync(path.join(dir, 'compiler-summary.json'), JSON.stringify({
      compilerVersion: solc.version(),
      openzeppelinVersion: JSON.parse(fs.readFileSync(path.join(ROOT, 'node_modules/@openzeppelin/contracts/package.json'), 'utf8')).version,
      settings: input.settings,
      creationBytecodeBytes: artifact.evm.bytecode.object.length / 2,
      deployedBytecodeBytes: artifact.evm.deployedBytecode.object.length / 2,
      warnings: (output.errors ?? []).filter(error => error.severity === 'warning').map(error => error.formattedMessage),
    }, null, 2) + '\n');
  }
  return output.contracts;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  compile({ writeArtifacts: true });
  console.log('Compiled TapeCommerceCheckout with pinned solc; artifacts and self-contained verification input written.');
}
