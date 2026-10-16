// SPDX-License-Identifier: GPL-3.0-only
import test from 'node:test';
import assert from 'node:assert/strict';
import { createApi } from '../public/ui.js';

test('default API stays relative and exported API uses the one reviewed HTTPS base without cookies or redirects', async () => {
  const calls = [];
  const fetchImpl = async (url, options) => { calls.push({ url, options }); return new Response(JSON.stringify({ ok: true })); };
  const sameOrigin = createApi({ fetchImpl });
  assert.deepEqual(await sameOrigin.request('/api/config'), { ok: true });
  assert.equal(calls[0].url, '/api/config');
  const portable = createApi({ fetchImpl, apiBase: 'https://tape.coco9.cc' });
  await portable.request('/api/demo/orders/abc/quote', { method: 'POST', token: 'scoped-demo-token', body: {} });
  assert.equal(calls[1].url, 'https://tape.coco9.cc/api/demo/orders/abc/quote');
  assert.equal(calls[1].options.headers['X-Demo-Token'], 'scoped-demo-token');
  assert.equal(calls[1].options.headers['content-type'], 'application/json');
  for (const { options } of calls) {
    assert.equal(options.credentials, 'omit');
    assert.equal(options.redirect, 'error');
    assert.equal(options.headers.Cookie, undefined);
    assert.equal(options.headers.Authorization, undefined);
  }
  sameOrigin.dispose(); portable.dispose();
});

test('untrusted bases and escaping paths never become a token destination', async () => {
  let calls = 0;
  const fetchImpl = async () => { calls++; throw Error('unexpected network'); };
  for (const apiBase of ['https://evil.example', 'http://tape.coco9.cc', '//tape.coco9.cc', 'https://tape.coco9.cc/',
    'https://tape.coco9.cc?x=1', 'https://tape.coco9.cc#x', 'https://user@tape.coco9.cc', 'https://tape.coco9.cc.evil.example',
    'https://TAPE.COCO9.CC', 'null', '*', null, 0]) {
    assert.throws(() => createApi({ fetchImpl, apiBase }), /invalid_api_base/);
  }
  const api = createApi({ fetchImpl, apiBase: 'https://tape.coco9.cc' });
  for (const path of ['https://evil.example/api/', '//evil.example/api/', '/api/../secret', '/api/%2e%2e/secret',
    '/api/..\\secret', '/api/config#fragment', '/api/config\n', null]) {
    await assert.rejects(api.request(path, { token: 'scoped-demo-token' }), /invalid_api_path/);
  }
  assert.equal(calls, 0);
  api.dispose();
});
