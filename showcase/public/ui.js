// SPDX-License-Identifier: GPL-3.0-only
export const escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[character]));

export function localized(value, language = 'zh') {
  if (typeof value === 'string') return value;
  return value?.[language] ?? value?.[language === 'zh' ? 'en' : 'zh'] ?? '';
}

export function formatWei(value, maxFraction = 8) {
  if (typeof value !== 'string' || !/^(0|[1-9][0-9]*)$/.test(value) || value.length > 78) return '—';
  const padded = value.padStart(19, '0');
  const whole = padded.slice(0, -18);
  const allFraction = padded.slice(-18);
  const fraction = allFraction.slice(0, maxFraction).replace(/0+$/, '');
  if (whole === '0' && !fraction && /[1-9]/.test(allFraction)) return `< 0.${'0'.repeat(maxFraction - 1)}1`;
  return `${whole}${fraction ? '.' + fraction : ''}${/[1-9]/.test(allFraction.slice(maxFraction)) ? '…' : ''}`;
}

export function exactOKB(value) {
  if (typeof value !== 'string' || !/^[0-9]{1,78}$/.test(value)) return '—';
  const padded = value.padStart(19, '0');
  const fraction = padded.slice(-18).replace(/0+$/, '');
  return padded.slice(0, -18) + (fraction ? '.' + fraction : '');
}

export function shortAddress(value) {
  return typeof value === 'string' && /^0x[a-fA-F0-9]{40}$/.test(value) ? `${value.slice(0, 6)}…${value.slice(-4)}` : '—';
}

export function explorerAddress(value) {
  return typeof value === 'string' && /^0x[a-fA-F0-9]{40}$/.test(value) ? `https://www.oklink.com/xlayer/address/${value}` : null;
}

export function remainingSeconds(deadline, nowMs = Date.now()) {
  if (typeof deadline !== 'string' || !/^[0-9]{1,13}$/.test(deadline)) return 0;
  const seconds = Number(deadline);
  if (!Number.isSafeInteger(seconds)) return 0;
  return Math.max(0, seconds - Math.floor(nowMs / 1000));
}

export const PENDING_STATES = new Set(['submitted', 'confirming']);
export const ORDER_STATES = new Set(['created', 'quoted', 'submitted', 'confirming', 'delivered', 'review', 'expired']);
const STORAGE_KEY = 'tapecommerce-demo-order-v1';

export function readSession(storage) {
  try {
    const value = JSON.parse(storage.getItem(STORAGE_KEY));
    if (!value || typeof value.id !== 'string' || !/^[a-zA-Z0-9_-]{1,100}$/.test(value.id)
      || typeof value.token !== 'string' || !/^[a-zA-Z0-9._~-]{16,512}$/.test(value.token)) return null;
    return { id: value.id, token: value.token };
  } catch { return null; }
}

export function saveSession(storage, value) {
  try { storage.setItem(STORAGE_KEY, JSON.stringify({ id: value.id, token: value.token })); return true; }
  catch { return false; }
}

export function clearSession(storage) {
  try { storage.removeItem(STORAGE_KEY); } catch { /* Private browsing may disallow storage. */ }
}

// The static exporter may set this one reviewed HTTPS origin in its output.
// Never derive a token destination from a URL parameter, storage or mutable config.
export function createApi({ fetchImpl = globalThis.fetch, timeoutMs = 20_000, apiBase = '' } = {}) {
  if (apiBase !== '' && apiBase !== 'https://tape.coco9.cc') throw new Error('invalid_api_base');
  const requests = new Set();
  return {
    async request(path, { method = 'GET', body, token } = {}) {
      if (typeof path !== 'string' || !/^\/api\/[^\\#\s]*$/.test(path)
        || !new URL(path, 'https://api.invalid').pathname.startsWith('/api/')) throw new Error('invalid_api_path');
      const controller = new AbortController();
      requests.add(controller);
      const timeout = setTimeout(() => controller.abort(), timeoutMs);
      try {
        const headers = { accept: 'application/json' };
        if (body !== undefined) headers['content-type'] = 'application/json';
        if (token) headers['X-Demo-Token'] = token;
        const response = await fetchImpl(apiBase + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body),
          credentials: 'omit', cache: 'no-store', redirect: 'error', signal: controller.signal });
        let payload;
        try { payload = await response.json(); } catch { throw Object.assign(Error('invalid_response'), { code: 'invalid_response' }); }
        if (!response.ok) throw Object.assign(Error(payload?.error ?? 'request_failed'), { code: payload?.error ?? 'request_failed', status: response.status });
        return payload;
      } catch (error) {
        if (controller.signal.aborted) throw Object.assign(Error('request_timeout'), { code: 'request_timeout' });
        throw error;
      } finally {
        clearTimeout(timeout);
        requests.delete(controller);
      }
    },
    dispose() { for (const controller of requests) controller.abort(); requests.clear(); },
  };
}

export function validatedOrder(value) {
  if (!value || typeof value.id !== 'string' || !/^[a-zA-Z0-9_-]{1,100}$/.test(value.id) || !ORDER_STATES.has(value.status)) {
    throw Object.assign(Error('invalid_order'), { code: 'invalid_response' });
  }
  return value;
}

export function downloadFilename(value) {
  if (typeof value !== 'string') return 'tapecommerce-sample.txt';
  const sanitized = value.split(/[\\/]/).pop().replace(/[\u0000-\u001f\u007f]/g, '').slice(0, 180);
  return sanitized && sanitized !== '.' && sanitized !== '..' ? sanitized : 'tapecommerce-sample.txt';
}
