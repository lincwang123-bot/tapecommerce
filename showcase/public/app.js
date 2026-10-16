// SPDX-License-Identifier: GPL-3.0-only
import { copy } from './i18n.js';
import { escapeHTML as e, localized, formatWei, exactOKB, explorerAddress, remainingSeconds,
  PENDING_STATES, createApi, readSession, saveSession, clearSession, validatedOrder, downloadFilename } from './ui.js';

const root = document.querySelector('#app');
const announcer = document.querySelector('#announcer');
const api = createApi();
let disposed = false;
let pollTimer;
let tickTimer;
let identityGeneration = 0;
let orderGeneration = 0;
let refreshInFlight = false;
const objectURLs = new Set();
const downloadTimers = new Set();
let storage;
try { storage = window.sessionStorage; } catch { storage = null; }
let savedLanguage;
try { savedLanguage = storage?.getItem('tapecommerce-language'); } catch { /* Storage is optional. */ }
const state = {
  language: savedLanguage === 'en' ? 'en' : 'zh',
  tab: ['evidence', 'merchant'].includes(location.hash.slice(1)) ? location.hash.slice(1) : 'experience',
  config: null, products: [], loading: true, loadError: '',
  identity: null, identityMeta: null, identityBusy: false, identityError: '',
  processorNumber: '', tokenId: '',
  order: null, credential: null, busy: '', error: '', notice: '', delivery: null, downloaded: false,
};

const t = key => copy[state.language][key] ?? key;
const txt = value => e(localized(value, state.language));
const demoReady = () => state.config?.demo?.enabled === true && state.config?.sourceMode === 'isolated_evm';
const arrow = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12h14m-6-6 6 6-6 6"/></svg>';
const icon = name => {
  const shapes = {
    external: '<path d="M14 4h6v6m0-6-10 10"/><path d="M10 5H5a1 1 0 0 0-1 1v13a1 1 0 0 0 1 1h13a1 1 0 0 0 1-1v-5"/>',
    check: '<path d="m5 12 4 4L19 6"/>',
    download: '<path d="M12 3v12m-4-4 4 4 4-4M4 15v5h16v-5"/>',
    shield: '<path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6l8-3Z"/><path d="m8 12 3 3 5-6"/>',
    copy: '<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V4H4v12h4"/>',
    circle: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
    refresh: '<path d="M20 7v5h-5M4 17v-5h5"/><path d="M5.5 8a7 7 0 0 1 11.6-3L20 8M4 16l2.9 3A7 7 0 0 0 18.5 16"/>',
  };
  return `<svg viewBox="0 0 24 24" aria-hidden="true">${shapes[name] ?? shapes.circle}</svg>`;
};

function announce(key) { announcer.textContent = t(key); }

function errorKey(error, context) {
  const code = String(error?.code ?? error?.message ?? '');
  if (error?.status === 401 || error?.status === 403 || /unauthorized|invalid.*token|token.*invalid/.test(code)) return 'unauthorized';
  if (error?.status === 429 || code === 'rate_limited') return 'tooManyRequests';
  if (/fx_.*stale|quote_.*expired|invoice_expired|quote_expired/.test(code)) return 'staleQuote';
  if (/fx_|rate_|market_|price_/.test(code)) return 'sourceUnavailable';
  if (/demo_.*unavailable|demo_disabled|evm_unavailable/.test(code)) return 'demoUnavailable';
  if (/request_timeout|fetch|network/i.test(code)) return 'networkError';
  return context === 'identity' ? 'identityError' : context === 'delivery' ? 'downloadError' : 'requestFailed';
}

function formatTime(value) {
  const numeric = typeof value === 'string' && /^\d+$/.test(value) ? Number(value) : value;
  const date = new Date(numeric);
  if (!Number.isFinite(date.getTime())) return '—';
  return new Intl.DateTimeFormat(state.language === 'zh' ? 'zh-CN' : 'en-GB', {
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(date);
}

function quoteFor(order = state.order) {
  if (!order) return null;
  const quote = order.quote;
  if (quote?.amountWei && quote?.deadline) return quote;
  if (order.amountWei && order.deadline) return { amountWei: order.amountWei, deadline: order.deadline, fxSnapshot: order.fxSnapshot };
  return null;
}

function productForOrder() { return state.products.find(product => product.id === state.order?.productId); }

function header() {
  return `<header class="site-header"><a class="brand" href="#" aria-label="TapeCommerce"><img src="./mark.svg" alt="" width="32" height="32"><span>TapeCommerce</span></a>
    <nav aria-label="${state.language === 'zh' ? '主导航' : 'Main navigation'}"><button data-tab="experience">${t('navExperience')}</button><button data-tab="evidence">${t('navEvidence')}</button><button data-tab="merchant">${t('navMerchant')}</button></nav>
    <button class="language-button" data-action="language" aria-label="${state.language === 'zh' ? 'Switch to English' : '切换为中文'}">${t('language')}</button></header>`;
}

function hero() {
  const sample = state.config?.sampleIdentity;
  const sampleName = sample && /^(0|[1-9][0-9]*)$/.test(String(sample.processorNumber)) && /^[1-9][0-9]*$/.test(String(sample.tokenId))
    ? `${sample.tokenId}.2.${sample.processorNumber}.tape` : 'TapeOut';
  return `<section class="hero"><div class="hero-copy"><h1>${t('heroTitle')}</h1><p>${t('heroBody')}</p><div class="hero-actions"><button class="button primary" data-tab="experience">${t('heroPrimary')}${arrow}</button><button class="text-button" data-tab="evidence">${t('heroSecondary')}${icon('external')}</button></div><span class="hero-caption">${t('heroCaption')}</span></div>
    <div class="identity-visual" aria-label="${t('previewTitle')}"><div class="visual-backplate" aria-hidden="true"></div><div class="visual-sheet"><div class="sheet-top"><span class="sheet-wordmark">TapeCommerce</span><span class="sheet-symbol" aria-hidden="true">T<span>.</span></span></div>
    <div class="sheet-name">${e(sampleName)}</div><div class="sheet-rule"></div><div class="sheet-data"><span>${t('previewIdentity')}</span><strong>${t('thirdParty').split(' · ')[0]}</strong><span>${t('previewNetwork')}</span><strong>X Layer</strong></div>
    <button class="sheet-link" data-action="sample">${t('previewOpen')}${arrow}</button></div><div class="visual-footnote"><span class="proof-symbol">${icon('shield')}</span><div><strong>${t('previewTitle')}</strong><span>${t('previewBody')}</span></div></div></div></section>`;
}

function tabs() {
  return `<div class="workspace-heading"><div><h2>${t('workspaceTitle')}</h2><p>${t('workspaceBody')}</p></div><div class="tabs" role="tablist" aria-label="${state.language === 'zh' ? '体验方式' : 'Explore'}">${[['experience', 'buyerTab'], ['evidence', 'evidenceTab'], ['merchant', 'merchantTab']].map(([id, label]) => `<button id="tab-${id}" role="tab" aria-selected="${state.tab === id}" aria-controls="panel-${id}" tabindex="${state.tab === id ? '0' : '-1'}" data-tab="${id}">${t(label)}</button>`).join('')}</div></div>`;
}

function sampleArtwork(product, index) {
  const title = txt(product.title);
  return `<div class="product-art art-${index % 3}" aria-hidden="true"><div class="sample-document"><span>TapeCommerce</span><div class="document-lines"><i></i><i></i><i></i></div><strong>${title}</strong><small>${e(product.format || 'DIGITAL EDITION')}</small></div><div class="sample-sliver"></div></div>`;
}

function catalogue() {
  return `<div class="catalogue"><div class="section-heading"><h3>${t('catalogueTitle')}</h3><p>${t('catalogueBody')}</p></div><div class="product-list">${state.products.map((product, index) => {
    const selected = state.order?.productId === product.id;
    return `<article class="product ${selected ? 'selected' : ''}">${sampleArtwork(product, index)}<div class="product-body"><div class="product-meta"><span>${e(product.format || t('fileLabel'))}</span><span>${t('demoPrice')}</span></div><h4>${txt(product.title)}</h4><p>${txt(product.description)}</p><div class="product-bottom"><strong>${e(product.amountUSDT)} <small>USDT</small></strong><button class="button ${selected ? 'secondary' : 'dark'}" data-product="${e(product.id)}" ${state.busy || selected || PENDING_STATES.has(state.order?.status) || !demoReady() ? 'disabled' : ''}>${selected ? icon('check') + t('selectedProduct') : t('selectProduct') + arrow}</button></div></div></article>`;
  }).join('') || `<p class="empty-message">${t('emptyCatalogue')}</p>`}</div></div>`;
}

function progress() {
  const status = state.order?.status;
  const step = status === 'delivered' ? 4 : PENDING_STATES.has(status) ? 2 : status === 'quoted' ? 2 : state.order ? 1 : 0;
  return `<ol class="order-progress" aria-label="${t('orderTitle')}">${['stepChoose', 'stepQuote', 'stepPay', 'stepReceive'].map((label, index) => `<li class="${index < step ? 'complete' : index === step ? 'current' : ''}" ${index === step ? 'aria-current="step"' : ''}><span>${index < step ? icon('check') : String(index + 1).padStart(2, '0')}</span><small>${t(label)}</small></li>`).join('')}</ol>`;
}

function quoteView(quote) {
  const fx = quote.fxSnapshot ?? state.order?.fxSnapshot;
  return `<div class="quote-amount"><span>${t('amountOKB')}</span><strong title="${e(exactOKB(String(quote.amountWei)))} OKB">${e(formatWei(String(quote.amountWei)))} <small>OKB</small></strong><div class="quote-timer">${icon('circle')}<span>${t('quoteExpires')}</span><strong data-countdown>${remainingSeconds(String(quote.deadline))}</strong><span>${t('seconds')}</span></div></div>
    <details class="quote-details"><summary>${t('quoteMore')}</summary><dl><div><dt>${t('amountOKB')}</dt><dd class="break-anywhere">${e(exactOKB(String(quote.amountWei)))} OKB</dd></div>${fx ? `<div><dt>${t('quoteSource')}</dt><dd>OKX · OKB-USDT · last</dd></div><div><dt>${t('quoteRate')}</dt><dd>1 OKB = ${e(fx.rateUSDTPerOKB)} USDT</dd></div><div><dt>${t('quoteObserved')}</dt><dd>${formatTime(fx.sourceTimestampMs)}</dd></div>` : ''}</dl></details><p class="microcopy">${t('quotePolicy')}</p>`;
}

function orderPanel() {
  const order = state.order;
  const quote = quoteFor();
  const product = productForOrder();
  const localExpired = order?.status === 'quoted' && quote && remainingSeconds(String(quote.deadline)) === 0;
  let body;
  if (!order) {
    body = `<div class="order-empty"><span class="empty-corner" aria-hidden="true">${icon('download')}</span><h4>${t('orderEmptyTitle')}</h4><p>${t('orderEmptyBody')}</p></div>`;
  } else {
    const busy = Boolean(state.busy);
    body = `<div class="order-product"><div><span>${t('amountUSDT')}</span><strong>${txt(product?.title || order.productTitle || order.productId)}</strong></div><strong>${e(order.amountUSDT || product?.amountUSDT || '—')} <small>USDT</small></strong></div>`;
    if (order.status === 'created' || order.status === 'expired' || localExpired) {
      const expired = order.status === 'expired' || localExpired;
      if (expired) body += `<div class="inline-note"><strong>${t('expiredTitle')}</strong><p>${t('expiredBody')}</p></div>`;
      body += `<button class="button primary full-width" data-action="${expired ? 'renew' : 'quote'}" ${busy || !demoReady() ? 'disabled' : ''}>${state.busy === 'quote' ? '<span class="spinner"></span>' + t('gettingQuote') : t(expired ? 'renewQuote' : 'getQuote') + arrow}</button>`;
    } else if (order.status === 'quoted') {
      body += quote ? quoteView(quote) : `<p class="inline-error">${t('requestFailed')}</p>`;
      body += `<button class="button primary full-width" data-action="pay" ${busy || !quote || !demoReady() ? 'disabled' : ''}>${state.busy === 'pay' ? '<span class="spinner"></span>' + t('working') : t('demoPay') + arrow}</button><p class="payment-notice">${t('payNote')}</p>`;
      if (state.error) body += `<button class="text-button" data-action="refresh" ${busy ? 'disabled' : ''}>${icon('refresh')}${t('refreshStatus')}</button>`;
    } else if (PENDING_STATES.has(order.status)) {
      body += `<div class="order-result pending"><div class="result-symbol"><span class="spinner"></span></div><h4>${t('pendingTitle')}</h4><p>${t('pendingBody')}</p></div><button class="button secondary full-width" data-action="refresh" ${busy ? 'disabled' : ''}>${icon('refresh')}${t('refreshStatus')}</button>`;
    } else if (order.status === 'delivered') {
      body += `<div class="order-result success"><div class="result-symbol">${icon('check')}</div><h4>${t('deliveredTitle')}</h4><p>${t('deliveredBody')}</p></div><button class="button primary full-width" data-action="download" ${busy ? 'disabled' : ''}>${state.busy === 'download' ? '<span class="spinner"></span>' + t('downloading') : icon('download') + t(state.downloaded ? 'downloadAgain' : 'download')}</button>`;
      if (state.downloaded) body += `<p class="download-feedback">${icon('check')}${t('downloaded')}</p>`;
      if (state.delivery?.licenseCode) body += `<div class="license"><span>${t('license')}</span><div><code>${e(state.delivery.licenseCode)}</code><button class="icon-button" data-action="copy-license" aria-label="${t('copyLicense')}" title="${t('copyLicense')}">${icon('copy')}</button></div><small>${t('licenseNote')}</small></div>`;
    } else if (order.status === 'review') {
      body += `<div class="order-result review"><div class="result-symbol">${icon('shield')}</div><h4>${t('reviewTitle')}</h4><p>${t('reviewBody')}</p></div><button class="button secondary full-width" data-action="refresh" ${busy ? 'disabled' : ''}>${icon('refresh')}${t('refreshStatus')}</button>`;
    }
    const hash = order.txHash || order.receipt?.txHash;
    if (typeof hash === 'string' && /^0x[a-fA-F0-9]{64}$/.test(hash)) body += `<details class="receipt-detail"><summary>${t('receiptLabel')}</summary><code>${e(hash)}</code><p>${t('localReceipt')}</p></details>`;
    body += `<div class="order-footer"><span>${t('orderReference')} <code>${e(order.id.slice(0, 14))}…</code></span><button class="text-button" data-action="reset" ${busy || PENDING_STATES.has(order.status) ? 'disabled' : ''}>${t('newOrder')}</button></div>`;
  }
  return `<aside class="order-panel" aria-labelledby="order-title"><div class="order-heading"><h3 id="order-title">${t('orderTitle')}</h3>${order ? `<span class="order-state state-${e(order.status)}">${t('status_' + order.status)}</span>` : ''}</div>${progress()}${state.busy === 'create' ? `<p class="working-note"><span class="spinner"></span>${t('creating')}</p>` : ''}${body}${state.error ? `<p class="inline-error" role="alert">${t(state.error)}</p>` : ''}${state.notice ? `<p class="inline-note subtle" role="status">${t(state.notice)}</p>` : ''}</aside>`;
}

function buyerPanel() {
  return `<section id="panel-experience" role="tabpanel" aria-labelledby="tab-experience"><div class="demo-banner"><span class="demo-dot" aria-hidden="true"></span><div><strong>${t('demoTitle')}</strong><p>${t('demoBody')}</p></div></div><div class="buyer-layout">${catalogue()}${orderPanel()}</div></section>`;
}

function addressRow(label, value) {
  const href = explorerAddress(value);
  return `<div><dt>${t(label)}</dt><dd>${href ? `<a href="${e(href)}" target="_blank" rel="noopener noreferrer" title="${e(value)}"><span>${e(value)}</span>${icon('external')}</a>` : '—'}</dd></div>`;
}

function evidencePanel() {
  const identity = state.identity;
  const block = Number.isSafeInteger(identity?.blockNumber) && identity.blockNumber >= 0 ? identity.blockNumber : null;
  return `<section id="panel-evidence" role="tabpanel" aria-labelledby="tab-evidence" class="evidence-layout"><div class="evidence-intro"><h3>${t('evidenceTitle')}</h3><p>${t('evidenceBody')}</p>
    <form id="identity-form"><div class="input-row"><label>${t('processorLabel')}<input name="processorNumber" inputmode="numeric" pattern="(0|[1-9][0-9]*)" maxlength="77" value="${e(state.processorNumber)}" placeholder="0" required autocomplete="off"></label><label>${t('tokenLabel')}<input name="tokenId" inputmode="numeric" pattern="[1-9][0-9]*" maxlength="77" value="${e(state.tokenId)}" placeholder="1" required autocomplete="off"></label></div><button class="button primary full-width" ${state.identityBusy ? 'disabled' : ''}>${state.identityBusy ? '<span class="spinner"></span>' + t('querying') : t('lookup') + arrow}</button></form>
    <button class="text-button sample-button" data-action="sample" ${state.identityBusy || !state.config?.sampleIdentity ? 'disabled' : ''}>${t('sampleButton')}${arrow}</button><p class="microcopy">${t('identityReadOnly')}</p>${state.identityError ? `<p class="inline-error" role="alert">${t(state.identityError)}</p>` : ''}</div>
    <div class="identity-record" aria-live="polite">${identity ? `<div class="record-heading"><div><span>${t('identityName')}</span><h4>${e(identity.name)}</h4></div><span class="chain-label">X Layer</span></div><p class="record-provenance">${t(state.identityMeta?.sampleIsThirdParty ? 'thirdParty' : 'publicRecord')}</p><dl class="record-fields">${addressRow('holder', identity.holder)}${addressRow('container', identity.container)}${addressRow('processorContract', identity.processor)}<div><dt>${t('activation')}</dt><dd><span class="state-dot ${identity.opened ? 'on' : ''}"></span>${t(identity.opened ? 'opened' : 'notOpened')}<span class="record-divider">/</span>${t(identity.live ? 'live' : 'notLive')}</dd></div><div><dt>${t('blockNumber')}</dt><dd>${block === null ? '—' : `<a href="https://www.oklink.com/xlayer/block/${block}" target="_blank" rel="noopener noreferrer">${block.toLocaleString('en-US')}${icon('external')}</a>`}</dd></div>${identity.checkedAt ? `<div><dt>${t('checkedAt')}</dt><dd>${formatTime(identity.checkedAt)}</dd></div>` : ''}</dl><div class="record-bottom">${icon('shield')}<p>${t('identityBoundary')}</p></div>` : `<div class="identity-empty">${icon('shield')}<h4>TapeOut / X Layer</h4><p>${t('identityEmpty')}</p></div>`}</div></section>`;
}

function featureStatus(value) {
  const status = value?.status ?? value;
  if (status === true || ['ready', 'available', 'complete', 'completed', 'enabled', 'verified'].includes(status)) return ['available', 'available'];
  if (['demo', 'demo_ready', 'sandbox', 'demo_only'].includes(status)) return ['demoOnly', 'demo'];
  if (['planned', 'in_progress', 'partial'].includes(status)) return ['planned', 'planned'];
  return ['notReady', 'unavailable'];
}

function merchantPanel() {
  const config = state.config;
  const steps = config.merchantSteps ?? [];
  const map = [
    ['template', 'merchantStepTemplate', 'merchantStepTemplateBody'],
    ['products', 'merchantStepProduct', 'merchantStepProductBody'],
    ['identity', 'merchantStepIdentity', 'merchantStepIdentityBody'],
    ['publish', 'merchantStepPublish', 'merchantStepPublishBody'],
  ];
  const feature = config.features ?? {};
  const features = [
    ['featureIdentity', feature.identity ?? steps.find(step => step.id === 'identity')],
    ['featureDemo', demoReady() ? 'demo' : false],
    ['featureLive', config.live?.ready],
    ['featurePublish', feature.publication ?? steps.find(step => step.id === 'publish')],
  ];
  let liveURL = null;
  try { if (typeof config.live?.checkoutUrl === 'string' && config.live.checkoutUrl.length) { const url = new URL(config.live.checkoutUrl, location.origin); if (config.live.ready === true && url.origin === location.origin && url.protocol === location.protocol) liveURL = url.href; } } catch { /* Only an explicitly configured same-origin checkout may be linked. */ }
  return `<section id="panel-merchant" role="tabpanel" aria-labelledby="tab-merchant"><div class="merchant-intro"><h3>${t('merchantTitle')}</h3><p>${t('merchantBody')}</p></div><ol class="merchant-steps">${map.map(([id, title, body], index) => {
    const row = steps.find(step => step.id === id || (id === 'products' && step.id === 'product'));
    const [label, kind] = featureStatus(row);
    return `<li><span class="merchant-number">${String(index + 1).padStart(2, '0')}</span><h4>${t(title)}</h4><p>${t(body)}</p><span class="feature-state ${kind}">${t(label)}</span></li>`;
  }).join('')}</ol><div class="merchant-bottom"><div class="business-note"><h4>${t('merchantCommerceTitle')}</h4><p>${t('merchantCommerceBody')}</p></div><div class="feature-list"><h4>${t('availability')}</h4>${features.map(([label, value]) => { const [status, kind] = featureStatus(value); return `<div><span>${t(label)}</span><strong class="feature-state ${kind}">${t(status)}</strong></div>`; }).join('')}${liveURL ? `<a href="${e(liveURL)}" class="button primary">${t('liveCheckout')}${arrow}</a>` : `<p class="microcopy">${t('liveUnavailable')}</p>`}</div></div></section>`;
}

function trust() {
  return `<section class="trust-section"><h2>${t('trustTitle')}</h2><div class="trust-grid">${[['01', 'trustChain', 'trustChainBody'], ['02', 'trustService', 'trustServiceBody'], ['03', 'trustDemo', 'trustDemoBody']].map(([number, title, body]) => `<article><span>${number}</span><h3>${t(title)}</h3><p>${t(body)}</p></article>`).join('')}</div></section>`;
}

function render() {
  if (disposed) return;
  document.documentElement.lang = state.language === 'zh' ? 'zh-CN' : 'en';
  document.title = state.language === 'zh' ? 'TapeCommerce · 让店铺有据可查' : 'TapeCommerce · Your store. With proof.';
  let content;
  if (state.loading) content = `<main class="page-loading" id="workspace" aria-busy="true"><span class="spinner"></span><p>${t('loading')}</p></main>`;
  else if (state.loadError) content = `<main class="page-error" id="workspace"><h1>${t('errorTitle')}</h1><p role="alert">${t(state.loadError)}</p><button class="button primary" data-action="retry">${t('retry')}${arrow}</button></main>`;
  else content = `<main>${hero()}<section class="workspace" id="workspace">${tabs()}${state.tab === 'experience' ? buyerPanel() : state.tab === 'evidence' ? evidencePanel() : merchantPanel()}</section>${trust()}</main>`;
  root.innerHTML = `<div class="site-shell">${header()}${content}<footer class="site-footer"><a class="brand" href="#"><img src="./mark.svg" alt="" width="26" height="26"><span>TapeCommerce</span></a><p>${t('footer')}</p><small>${t('footerNote')}</small></footer></div>`;
}

function setTab(tab, scroll = true) {
  if (!['experience', 'evidence', 'merchant'].includes(tab)) return;
  state.tab = tab;
  history.replaceState(null, '', `#${tab}`);
  render();
  if (scroll) document.querySelector('#workspace')?.scrollIntoView({ behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth', block: 'start' });
}

function setOrder(payload) {
  const order = validatedOrder(payload?.order ?? payload);
  if (state.credential && order.id !== state.credential.id) throw Object.assign(Error('invalid_response'), { code: 'invalid_response' });
  state.order = order;
  schedulePoll();
}

function schedulePoll() {
  clearTimeout(pollTimer);
  if (!disposed && state.credential && PENDING_STATES.has(state.order?.status)) pollTimer = setTimeout(() => refreshOrder(true), 1800);
}

async function refreshOrder(automatic = false) {
  if (!state.credential || disposed || refreshInFlight) return;
  refreshInFlight = true;
  clearTimeout(pollTimer);
  const generation = orderGeneration;
  const credential = state.credential;
  if (!automatic) { state.busy = 'refresh'; state.error = ''; render(); }
  try {
    const result = await api.request(`/api/demo/orders/${encodeURIComponent(credential.id)}`, { token: credential.token });
    if (disposed || generation !== orderGeneration) return;
    const previous = state.order?.status;
    setOrder(result);
    state.error = '';
    if (previous !== state.order.status && state.order.status === 'delivered') announce('deliveredTitle');
  } catch (error) {
    if (disposed || generation !== orderGeneration) return;
    if (error?.status === 401 || error?.status === 403 || error?.status === 404) {
      state.order = null; state.credential = null; clearSession(storage); state.notice = 'sessionLost';
    } else {
      state.error = automatic ? 'pendingNetwork' : errorKey(error);
    }
    // A network failure remains pending and is recoverable by the manual check.
    clearTimeout(pollTimer);
  } finally {
    refreshInFlight = false;
    if (!disposed && generation === orderGeneration) { if (!automatic) state.busy = ''; render(); }
  }
}

async function chooseProduct(id) {
  if (state.busy || disposed || PENDING_STATES.has(state.order?.status) || !state.products.some(product => product.id === id) || !demoReady()) return false;
  const generation = ++orderGeneration;
  clearTimeout(pollTimer);
  state.busy = 'create'; state.error = ''; state.notice = ''; render();
  try {
    const result = await api.request('/api/demo/orders', { method: 'POST', body: { productId: id } });
    if (disposed || generation !== orderGeneration) return false;
    const order = validatedOrder(result.order);
    if (typeof result.token !== 'string' || !/^[A-Za-z0-9._~-]{16,512}$/.test(result.token)) throw Object.assign(Error('invalid_response'), { code: 'invalid_response' });
    state.credential = { id: order.id, token: result.token };
    state.delivery = null; state.downloaded = false;
    if (!saveSession(storage, state.credential)) state.notice = 'noStorage';
    setOrder(result);
    return true;
  } catch (error) {
    if (!disposed && generation === orderGeneration) state.error = errorKey(error);
    return false;
  } finally {
    if (!disposed && generation === orderGeneration) { state.busy = ''; render(); }
  }
}

async function renewQuote() {
  const quote = quoteFor();
  const expired = state.order?.status === 'expired' || (state.order?.status === 'quoted' && quote && remainingSeconds(String(quote.deadline)) === 0);
  if (!expired || state.busy || disposed) return;
  // A signed invoice is immutable. A new order preserves the old invoice's
  // identity, even if its transaction is still somewhere in transit.
  if (await chooseProduct(state.order.productId)) await orderAction('quote');
}

async function orderAction(action) {
  if (state.busy || !state.credential || disposed) return;
  if (action === 'pay' && (state.order?.status !== 'quoted' || !quoteFor() || remainingSeconds(String(quoteFor().deadline)) <= 0)) { state.error = 'staleQuote'; render(); return; }
  const generation = orderGeneration;
  state.busy = action; state.error = ''; state.notice = ''; render();
  try {
    const result = await api.request(`/api/demo/orders/${encodeURIComponent(state.credential.id)}/${action}`, { method: 'POST', body: {}, token: state.credential.token });
    if (disposed || generation !== orderGeneration) return;
    setOrder(result);
    if (state.order.status === 'delivered') announce('deliveredTitle');
  } catch (error) {
    if (!disposed && generation === orderGeneration) {
      const failure = errorKey(error);
      state.error = failure;
      // A lost HTTP response does not prove the payment failed. Read the
      // authoritative order before offering another attempt.
      if (action === 'pay' || error?.code === 'order_quote_expired_create_new') {
        await refreshOrder(true);
        if (state.order?.status === 'quoted') state.error = failure;
      }
    }
  } finally {
    if (!disposed && generation === orderGeneration) { state.busy = ''; render(); }
  }
}

async function receiveDownload() {
  if (state.busy || !state.credential || state.order?.status !== 'delivered') return;
  const generation = orderGeneration;
  state.busy = 'download'; state.error = ''; render();
  try {
    const result = await api.request(`/api/demo/orders/${encodeURIComponent(state.credential.id)}/delivery`, { token: state.credential.token });
    if (disposed || generation !== orderGeneration) return;
    if (typeof result.base64 !== 'string' || !result.base64.length || result.base64.length > 15_000_000 || !/^[A-Za-z0-9+/]*={0,2}$/.test(result.base64)) throw Error('invalid_delivery');
    const bytes = Uint8Array.from(atob(result.base64), character => character.charCodeAt(0));
    const blob = new Blob([bytes], { type: typeof result.contentType === 'string' ? result.contentType : 'application/octet-stream' });
    const url = URL.createObjectURL(blob);
    objectURLs.add(url);
    const link = document.createElement('a');
    link.href = url; link.download = downloadFilename(result.filename); link.hidden = true;
    document.body.append(link); link.click(); link.remove();
    const timer = setTimeout(() => { URL.revokeObjectURL(url); objectURLs.delete(url); downloadTimers.delete(timer); }, 1500);
    downloadTimers.add(timer);
    state.delivery = { filename: result.filename, licenseCode: typeof result.licenseCode === 'string' ? result.licenseCode : null };
    state.downloaded = true;
    announce('downloaded');
  } catch (error) {
    if (!disposed && generation === orderGeneration) state.error = errorKey(error, 'delivery');
  } finally {
    if (!disposed && generation === orderGeneration) { state.busy = ''; render(); }
  }
}

async function queryIdentity(useSample = false) {
  if (state.identityBusy || disposed) return;
  if (useSample) {
    const sample = state.config?.sampleIdentity;
    if (!sample) return;
    state.processorNumber = String(sample.processorNumber); state.tokenId = String(sample.tokenId);
    setTab('evidence');
  }
  const processorNumber = state.processorNumber.trim();
  const tokenId = state.tokenId.trim();
  if (!/^(0|[1-9][0-9]{0,76})$/.test(processorNumber) || !/^[1-9][0-9]{0,76}$/.test(tokenId)) { state.identityError = 'invalidIdentity'; render(); return; }
  const generation = ++identityGeneration;
  state.identityBusy = true; state.identityError = ''; state.identity = null; render();
  try {
    const result = await api.request(`/api/identity?processorNumber=${encodeURIComponent(processorNumber)}&tokenId=${encodeURIComponent(tokenId)}`);
    if (disposed || generation !== identityGeneration) return;
    if (result.provenance !== 'xlayer_mainnet_readonly' || result.identity?.chainId !== 196) throw Error('invalid_identity');
    state.identity = result.identity; state.identityMeta = result;
  } catch (error) {
    if (!disposed && generation === identityGeneration) state.identityError = errorKey(error, 'identity');
  } finally {
    if (!disposed && generation === identityGeneration) { state.identityBusy = false; render(); }
  }
}

async function initialize() {
  state.loading = true; state.loadError = ''; render();
  try {
    const [config, catalog] = await Promise.all([api.request('/api/config'), api.request('/api/catalog')]);
    if (disposed) return;
    if (!config || !Array.isArray(catalog.products)) throw Error('invalid_response');
    state.config = config; state.products = catalog.products;
    state.processorNumber = String(config.sampleIdentity?.processorNumber ?? '');
    state.tokenId = String(config.sampleIdentity?.tokenId ?? '');
    state.credential = readSession(storage);
    if (state.credential) { state.notice = 'restored'; await refreshOrder(); }
  } catch (error) {
    if (!disposed) state.loadError = errorKey(error);
  } finally {
    if (!disposed) { state.loading = false; render(); }
  }
}

root.addEventListener('click', async event => {
  const button = event.target.closest('button, a[data-tab]');
  if (!button || button.disabled) return;
  if (button.dataset.tab) { setTab(button.dataset.tab); return; }
  if (button.dataset.product) { await chooseProduct(button.dataset.product); return; }
  const action = button.dataset.action;
  if (action === 'language') {
    state.language = state.language === 'zh' ? 'en' : 'zh';
    try { storage?.setItem('tapecommerce-language', state.language); } catch { /* Optional preference. */ }
    render();
  } else if (action === 'sample') await queryIdentity(true);
  else if (action === 'renew') await renewQuote();
  else if (action === 'quote' || action === 'pay') await orderAction(action);
  else if (action === 'refresh') await refreshOrder();
  else if (action === 'download') await receiveDownload();
  else if (action === 'retry') await initialize();
  else if (action === 'reset' && !state.busy) {
    ++orderGeneration; clearTimeout(pollTimer); clearSession(storage);
    state.order = null; state.credential = null; state.error = ''; state.notice = ''; state.delivery = null; state.downloaded = false; render();
  } else if (action === 'copy-license' && state.delivery?.licenseCode) {
    try { await navigator.clipboard.writeText(state.delivery.licenseCode); announce('copied'); }
    catch { state.error = 'copyError'; render(); }
  }
});

root.addEventListener('submit', event => {
  if (event.target.id !== 'identity-form') return;
  event.preventDefault();
  const data = new FormData(event.target);
  state.processorNumber = String(data.get('processorNumber')); state.tokenId = String(data.get('tokenId'));
  queryIdentity();
});

root.addEventListener('input', event => {
  if (event.target.name === 'processorNumber') state.processorNumber = event.target.value;
  if (event.target.name === 'tokenId') state.tokenId = event.target.value;
});

root.addEventListener('keydown', event => {
  if (event.target.getAttribute('role') !== 'tab' || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
  event.preventDefault();
  const tabs = ['experience', 'evidence', 'merchant'];
  const index = tabs.indexOf(state.tab);
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? 2 : (index + (event.key === 'ArrowRight' ? 1 : 2)) % 3;
  setTab(tabs[next], false); document.querySelector(`#tab-${tabs[next]}`)?.focus();
});

tickTimer = setInterval(() => {
  const quote = quoteFor();
  if (!quote || state.order?.status !== 'quoted' || disposed) return;
  const left = remainingSeconds(String(quote.deadline));
  const countdown = root.querySelector('[data-countdown]');
  if (countdown) countdown.textContent = String(left);
  if (left === 0 && root.querySelector('[data-action="pay"]')) render();
}, 1000);

window.addEventListener('pagehide', () => {
  disposed = true; ++identityGeneration; ++orderGeneration;
  clearInterval(tickTimer); clearTimeout(pollTimer); api.dispose();
  for (const timer of downloadTimers) clearTimeout(timer);
  downloadTimers.clear();
  for (const url of objectURLs) URL.revokeObjectURL(url);
  objectURLs.clear();
}, { once: true });

window.addEventListener('pageshow', event => { if (event.persisted && disposed) location.reload(); });
initialize();
