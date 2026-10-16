// SPDX-License-Identifier: GPL-3.0-only
import test from 'node:test';
import assert from 'node:assert/strict';
import { startSandbox } from '../sandbox.mjs';

test('missing Go binary rejects and shuts down the already started local chain', { timeout: 25000 }, async () => {
  const pids = [], previous = process.env.TAPECOMMERCE_BRIDGE_BIN;
  process.env.TAPECOMMERCE_BRIDGE_BIN = '/nonexistent/tapecommerce-test-bridge';
  try {
    await assert.rejects(startSandbox({ onProcess: pid => pids.push(pid) }), /demo_bridge_missing/);
    assert.ok(pids.length >= 1);
    for (const pid of pids) assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
  } finally {
    if (previous === undefined) delete process.env.TAPECOMMERCE_BRIDGE_BIN;
    else process.env.TAPECOMMERCE_BRIDGE_BIN = previous;
  }
});
