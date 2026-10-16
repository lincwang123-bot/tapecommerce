// SPDX-License-Identifier: GPL-3.0-only
import {renderStorefront, renderProduct, themes, themeFor} from '/storefront-ui/index.js';
import {bindNavigation} from '/storefront-ui/navigation.js';
import {bindLaptop} from '/storefront-ui/laptop.js';
import {commerceHeading, commerceColumns, purchaseConfirmation, orderSummary} from '/storefront-ui/commerce.js';
import { copy } from './i18n.js';
import { escapeHTML as e, localized, formatWei, exactOKB, explorerAddress, remainingSeconds,
  PENDING_STATES, createApi, readSession, saveSession, clearSession, validatedOrder, downloadFilename } from './ui.js';

const root = document.body;
const main = document.querySelector('main');
let stopNavigation = () => {}, stopLaptop = () => {};
let searchText = '';
let selectedCategory = '';
let theme = 'minimal';
try { theme = themeFor(sessionStorage.getItem('tapecommerce-demo-theme')).id; } catch {}
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

function addressRow(label, value) {
  const href = explorerAddress(value);
  return `<div><dt>${t(label)}</dt><dd>${href ? `<a href="${e(href)}" target="_blank" rel="noopener noreferrer" title="${e(value)}"><span>${e(value)}</span>${icon('external')}</a>` : '—'}</dd></div>`;
}

function evidencePanel() {
  const identity = state.identity;
  const block = Number.isSafeInteger(identity?.blockNumber) && identity.blockNumber >= 0 ? identity.blockNumber : null;
  return `<section id="panel-evidence" class="evidence-layout"><div class="evidence-intro"><h3>${t('evidenceTitle')}</h3><p>${t('evidenceBody')}</p>
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
  return `<section id="panel-merchant" ><div class="merchant-intro"><h3>${t('merchantTitle')}</h3><p>${t('merchantBody')}</p></div><ol class="merchant-steps">${map.map(([id, title, body], index) => {
    const row = steps.find(step => step.id === id || (id === 'products' && step.id === 'product'));
    const [label, kind] = featureStatus(row);
    return `<li><span class="merchant-number">${String(index + 1).padStart(2, '0')}</span><h4>${t(title)}</h4><p>${t(body)}</p><span class="feature-state ${kind}">${t(label)}</span></li>`;
  }).join('')}</ol><div class="merchant-bottom"><div class="business-note"><h4>${t('merchantCommerceTitle')}</h4><p>${t('merchantCommerceBody')}</p></div><div class="feature-list"><h4>${t('availability')}</h4>${features.map(([label, value]) => { const [status, kind] = featureStatus(value); return `<div><span>${t(label)}</span><strong class="feature-state ${kind}">${t(status)}</strong></div>`; }).join('')}${liveURL ? `<a href="${e(liveURL)}" class="button primary">${t('liveCheckout')}${arrow}</a>` : `<p class="microcopy">${t('liveUnavailable')}</p>`}</div></div></section>`;
}

function routeInfo() {
  const parts = location.hash.replace(/^#\/?/, '').split('/');
  return {page: parts[0] || 'shop', id: parts[1] ? (() => { try { return decodeURIComponent(parts[1]); } catch { return ''; } })() : ''};
}
const phrase = (zh,en) => state.language === 'zh' ? zh : en;
function demoBanner() { return `<p class="notice demo-notice"><strong>${t('demoTitle')}</strong> · ${t('demoBody')}</p>`; }
function storefront() {
  return `${demoBanner()}${renderStorefront({template:theme,brand:'TapeCommerce',categories:[{id:'samples',name:phrase('自有数字样品','Original digital samples')}],heroTitle:phrase('好用的数字产品，\n让好想法更进一步。','Digital tools for\nyour next good idea.')})}`;
}
function productPage(id) {
  const product = state.products.find(product => product.id === id);
  if (!product) return `<p class="notice">${phrase('未找到商品。','Product not found.')}</p><a href="#/shop">${phrase('返回商品','Back to store')}</a>`;
  return `<a class="back" href="#/shop">← ${phrase('返回商品','Back to store')}</a>${commerceHeading(localized(product.title,state.language),phrase('自有数字授权样品 · 演示订单','Original digital sample · Demo order'))}${commerceColumns(
    `<h2>${phrase('商品说明','Product details')}</h2><p class="description">${txt(product.description)}</p><p>${e(product.format)} · ${e(product.filename)}</p><div class="notice">${t('catalogueBody')}</div>`,
    `<h2>${phrase('购买选项','Purchase options')}</h2><form id="demo-buy-form" data-product-id="${e(product.id)}"><label class="field">${phrase('购买数量','Quantity')}<input type="number" value="1" min="1" max="1" readonly></label><p>${phrase('商品标价合计','Listed total')} <strong>${e(product.amountUSDT)} USDT</strong></p><fieldset class="payment-methods"><legend>${phrase('付款方式','Payment method')}</legend><label><input type="radio" name="method" checked><span><strong>${t('demoTitle')}</strong><small>${t('payNote')}</small></span></label></fieldset><button class="full" type="submit" ${!demoReady() || state.busy || PENDING_STATES.has(state.order?.status) ? 'disabled' : ''}>${phrase('确认价格并购买','Review order')}</button>${state.error ? `<p class="error" role="alert">${t(state.error)}</p>` : ''}</form>`)}${demoBanner()}`;
}
function showConfirmation(id) {
  const product = state.products.find(product => product.id === id);
  if (!product || state.busy) return;
  const dialog = document.createElement('dialog');
  dialog.innerHTML = purchaseConfirmation({title:localized(product.title,state.language),total:product.amountUSDT,heading:phrase('确认演示订单','Confirm demo order'),totalLabel:phrase('订单确认金额','Order total'),noteHTML:`<p class="notice">${t('payNote')}</p>`,methodsHTML:`<fieldset class="payment-methods"><legend>${phrase('付款方式','Payment method')}</legend><label><input type="radio" checked><span><strong>${t('demoTitle')}</strong><small>${phrase('使用测试余额，下一步核对实时 OKB 报价','Use a test balance; review the live OKB quote next')}</small></span></label></fieldset>`,actionsHTML:`<p class="error" hidden></p><div class="row"><button id="confirm-demo">${phrase('创建演示订单','Create demo order')}</button><button id="cancel-demo" class="secondary">${phrase('取消','Cancel')}</button></div>`});
  document.body.append(dialog); dialog.showModal();
  dialog.querySelector('#cancel-demo').onclick = () => dialog.close();
  dialog.addEventListener('close',() => dialog.remove());
  dialog.querySelector('#confirm-demo').onclick = async () => {
    dialog.querySelector('#confirm-demo').disabled = true;
    const result = await chooseProduct(id);
    if (result) { dialog.close(); location.hash = '/order/' + encodeURIComponent(state.order.id); }
    else { dialog.querySelector('#confirm-demo').disabled = false; const message = dialog.querySelector('.error'); message.hidden = false; message.textContent = t(state.error || 'requestFailed'); }
  };
}
function demoOrderPage(id) {
  if (!state.order || id !== state.order.id) return `${commerceHeading(phrase('演示订单','Demo order'))}<p class="notice">${t('sessionLost')}</p><a href="#/shop">${t('newOrder')}</a>`;
  const product = productForOrder();
  return `<a class="back" href="#/shop">← ${phrase('返回商品','Back to store')}</a>${commerceHeading(phrase('订单详情','Order details'),state.order.id)}${demoBanner()}${orderSummary({statusHTML:`<span class="badge">${t('status_' + state.order.status)}</span>`,total:state.order.amountUSDT,currency:'USDT',itemsHTML:`<p>${txt(product?.title || state.order.productId)} × 1</p>`})}${orderPanel()}`;
}
function fillCatalogue() {
  const list = main.querySelector('.products'); if (!list) return;
  const filtered = state.products.filter(product => `${localized(product.title,state.language)} ${localized(product.description,state.language)}`.toLowerCase().includes(searchText.toLowerCase()));
  list.innerHTML = filtered.map((product,index) => renderProduct({name:localized(product.title,state.language),description:localized(product.description,state.language),category:phrase('自有数字样品','Original digital sample'),price:product.amountUSDT+' USDT',href:'#/product/'+encodeURIComponent(product.id)},theme,index)).join('') || `<p class="notice">${t('emptyCatalogue')}</p>`;
  const count = main.querySelector('[data-catalog-count]'); if (count) count.textContent = String(filtered.length);
}
function render() {
  if (disposed) return;
  stopNavigation(); stopLaptop();
  document.documentElement.lang = state.language === 'zh' ? 'zh-CN' : 'en';
  const currentTheme = themeFor(theme); document.body.dataset.template = currentTheme.id; document.body.dataset.design = currentTheme.design;
  document.querySelector('meta[name="color-scheme"]').content = ['orbital','neon'].includes(currentTheme.design) ? 'dark' : 'light';
  document.title = 'TapeCommerce · ' + phrase('数字授权商店','Digital goods store');
  const header = document.querySelector('body > header');
  header.innerHTML = `<a class="brand" href="#/shop"><span class="brandmark">T</span><span>TapeCommerce</span><small>STORE</small></a><nav id="store-navigation" aria-label="${phrase('主导航','Main navigation')}"><a href="#/shop">${phrase('选购商品','Shop')}</a>${state.order ? `<a href="#/order/${e(state.order.id)}">${phrase('演示订单','Demo order')}</a>` : ''}<a href="#/evidence">${t('navEvidence')}</a><a href="#/merchant">${t('navMerchant')}</a></nav><button class="nav-toggle" aria-controls="store-navigation" aria-expanded="false" type="button">${phrase('菜单','Menu')}</button><button class="button subtle" data-action="language">${t('language')}</button>`;
  stopNavigation = bindNavigation(header);
  document.querySelector('.demo-toolbar').innerHTML = `<span>${phrase('原有六套商店模板 · 面向合法数字授权商家','Six original storefront themes · For licensed digital goods')}</span><label>${phrase('主题展示','Theme preview')} <select id="theme-select" aria-label="${phrase('主题展示','Theme preview')}">${themes.map(item=>`<option value="${item.id}" ${item.id===theme?'selected':''}>${e(item.name)} / ${item.design}</option>`).join('')}</select></label>`;
  const route = routeInfo();
  if (state.loading) main.innerHTML = `<p role="status">${t('loading')}</p>`;
  else if (state.loadError) main.innerHTML = `<p class="error" role="alert">${t(state.loadError)}</p><button data-action="retry">${t('retry')}</button>`;
  else if (route.page === 'product') main.innerHTML = productPage(route.id);
  else if (route.page === 'order') main.innerHTML = demoOrderPage(route.id);
  else if (route.page === 'evidence') main.innerHTML = `${commerceHeading(t('evidenceTitle'),t('evidenceBody'))}${evidencePanel()}`;
  else if (route.page === 'merchant' || route.page === 'support') main.innerHTML = `${commerceHeading(t('merchantTitle'),t('merchantBody'))}${merchantPanel()}`;
  else { main.innerHTML = storefront(); fillCatalogue(); stopLaptop = bindLaptop(main); }
  document.querySelector('body > footer').innerHTML = `<span>TapeCommerce</span><span>${t('footerNote')}</span><a href="#/merchant">${t('navMerchant')}</a><a href="#/evidence">${t('navEvidence')}</a>`;
}
function setTab(tab) { location.hash = '/' + (tab === 'experience' ? 'shop' : tab); render(); }

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
  if (await chooseProduct(state.order.productId)) { location.hash = '/order/' + encodeURIComponent(state.order.id); await orderAction('quote'); }
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
    state.order = null; state.credential = null; state.error = ''; state.notice = ''; state.delivery = null; state.downloaded = false; location.hash = '/shop'; render();
  } else if (action === 'copy-license' && state.delivery?.licenseCode) {
    try { await navigator.clipboard.writeText(state.delivery.licenseCode); announce('copied'); }
    catch { state.error = 'copyError'; render(); }
  }
});

root.addEventListener('submit', event => {
  if (event.target.id === 'demo-buy-form') { event.preventDefault(); showConfirmation(event.target.dataset.productId); return; }
  if (event.target.id !== 'identity-form') return;
  event.preventDefault();
  const data = new FormData(event.target);
  state.processorNumber = String(data.get('processorNumber')); state.tokenId = String(data.get('tokenId'));
  queryIdentity();
});

root.addEventListener('input', event => {
  if (event.target.matches('input.search')) { searchText = event.target.value; fillCatalogue(); }
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
  disposed = true; ++identityGeneration; ++orderGeneration; stopNavigation(); stopLaptop();
  clearInterval(tickTimer); clearTimeout(pollTimer); api.dispose();
  for (const timer of downloadTimers) clearTimeout(timer);
  downloadTimers.clear();
  for (const url of objectURLs) URL.revokeObjectURL(url);
  objectURLs.clear();
}, { once: true });

window.addEventListener('pageshow', event => { if (event.persisted && disposed) location.reload(); });
initialize();

root.addEventListener('change', event => { if (event.target.id !== 'theme-select') return; theme = themeFor(event.target.value).id; try { storage?.setItem('tapecommerce-demo-theme',theme); } catch {} render(); });
root.addEventListener('click', event => { const jump = event.target.closest('[data-catalog-jump]'); if (jump) { event.preventDefault(); main.querySelector('[data-catalog]')?.scrollIntoView({behavior:'smooth'}); } const category = event.target.closest('[data-category]'); if (category) { selectedCategory = category.dataset.category; main.querySelectorAll('[data-category]').forEach(button => (button.classList.toggle('active',button===category),button.setAttribute('aria-pressed',String(button===category)))); fillCatalogue(); } });
window.addEventListener('hashchange', () => { document.querySelectorAll('dialog').forEach(dialog => dialog.close()); render(); window.scrollTo({top:0,behavior:'instant'}); });
