// SPDX-License-Identifier: GPL-3.0-only
import {escapeHTML as e} from './storefront-ui/shared.js';
const ADDRESS = /^0x[0-9a-fA-F]{40}$/;
const HASH = /^0x[0-9a-fA-F]{64}$/;
const ZERO_ADDRESS = '0x' + '0'.repeat(40);
const FIELDS = [['invoiceId','bytes32'],['tenantHash','bytes32'],['payer','address'],['merchant','address'],['amountWei','uint256'],['deadline','uint64']];
const same = (a,b) => typeof a === 'string' && typeof b === 'string' && a.toLowerCase() === b.toLowerCase();
const address = value => ADDRESS.test(value) && !same(value,ZERO_ADDRESS);
function integer(value, bits = 256) {
  if (typeof value !== 'string' || !/^[1-9][0-9]*$/.test(value) || value.length > 78 || BigInt(value) >= 2n ** BigInt(bits)) throw Error('付款数据不完整，请刷新订单。');
  return BigInt(value);
}
export function exactOKB(value) {
  integer(value);
  const padded = value.padStart(19,'0'), fraction = padded.slice(-18).replace(/0+$/,'');
  return padded.slice(0,-18) + (fraction ? '.' + fraction : '');
}
export function liveConfig(config) {
  return config?.mode === 'live' && config.enabled === true && config.ready === true && config.available === true && config.chain_id === 196 && config.native_symbol === 'OKB' && address(config.checkout_address);
}
function cents(value) {
  const text = String(value);
  if (!/^(0|[1-9][0-9]*)(\.[0-9]{1,2})?$/.test(text)) throw Error('订单金额格式错误。');
  const [whole,fraction=''] = text.split('.');
  return BigInt(whole)*100n + BigInt(fraction.padEnd(2,'0'));
}
export function validateQuote(quote, config, payer, total, now = Date.now()) {
  const invoice = quote?.invoice, domain = quote?.domain;
  if (!liveConfig(config) || quote.chain_id !== 196 || !same(quote.checkout_address,config.checkout_address)
      || domain?.name !== 'TapeCommerceCheckout' || domain.version !== '1' || Number(domain.chainId) !== 196 || !same(domain.verifyingContract,config.checkout_address)
      || !Array.isArray(quote.types?.Invoice) || quote.types.Invoice.length !== FIELDS.length
      || !FIELDS.every(([name,type],i) => quote.types.Invoice[i]?.name === name && quote.types.Invoice[i]?.type === type)
      || !invoice || !HASH.test(invoice.invoiceId) || /^0x0+$/.test(invoice.invoiceId) || !HASH.test(invoice.tenantHash) || /^0x0+$/.test(invoice.tenantHash)
      || !address(invoice.payer) || !same(invoice.payer,payer) || !address(invoice.merchant) || same(invoice.merchant,config.checkout_address)
      || !same(quote.invoice_id,invoice.invoiceId) || quote.amount_wei !== invoice.amountWei || String(quote.deadline) !== invoice.deadline
      || !/^0x[0-9a-fA-F]{128}(1b|1c)$/i.test(quote.signature)) throw Error('付款报价校验失败，请刷新订单核查。');
  integer(invoice.amountWei);
  const deadline = integer(invoice.deadline,64);
  if (deadline <= BigInt(Math.floor(now/1000))) throw Error('报价已过期，请重新创建订单；如已发送交易，请先查询状态。');
  if (cents(quote.fx_snapshot?.amountUSDT) !== cents(total)) throw Error('付款报价与原订单金额不一致。');
  return quote;
}
// payOKB((bytes32,bytes32,address,address,uint256,uint64),bytes), fixed ABI.
export function encodePayOKB(invoice, signature) {
  if (!HASH.test(invoice?.invoiceId) || !HASH.test(invoice.tenantHash) || !address(invoice.payer) || !address(invoice.merchant)
      || !/^0x[0-9a-fA-F]{128}(1b|1c)$/i.test(signature)) throw Error('invalid_invoice');
  const word = value => value.toString(16).padStart(64,'0');
  return '0xc14fb694' + invoice.invoiceId.slice(2) + invoice.tenantHash.slice(2)
    + invoice.payer.slice(2).padStart(64,'0') + invoice.merchant.slice(2).padStart(64,'0')
    + word(integer(invoice.amountWei)) + word(integer(invoice.deadline,64)) + word(224n)
    + word(65n) + signature.slice(2).padEnd(192,'0');
}
const stateLabels = {quote_issued:'报价已签发',submitted:'交易已提交，等待核验',detected:'已发现交易，等待确认',fulfillment_pending:'付款已核验，等待交付',delivered:'交付已确认',review_required:'需要人工核查'};
export function mountTapeCommerceCheckout(element, {order, request, isCurrent = () => true, existing = false, onPaymentLock = () => {}, onOrderChanged = () => {}, provider = globalThis.ethereum, storage} = {}) {
  let disposed = false, generation = 0, busy = false, locked = existing, config, quote, status, account, hash, error = '', timer, tick;
  if (!storage) { try { storage = globalThis.sessionStorage; } catch {} }
  const base = `/orders/${encodeURIComponent(order.order_no)}/tapecommerce`;
  const storageKey = `tapecommerce-tx:${order.order_no}`;
  const live = () => !disposed && element.isConnected && isCurrent();
  const readSaved = () => { try { const value = JSON.parse(storage?.getItem(storageKey)); return HASH.test(value?.invoice_id) && (HASH.test(value?.transaction_hash) || value?.uncertain === true) ? value : null; } catch { return null; } };
  const remember = (value,required=false) => { try { storage?.setItem(storageKey,JSON.stringify(value)); if (required && storage?.getItem(storageKey) !== JSON.stringify(value)) throw Error('storage'); } catch { if (required) throw Error('浏览器无法保存付款恢复记录，请启用此站点的会话存储后再付款。'); } };
  const lock = () => { locked = true; if (live()) onPaymentLock(); };
  const activeState = () => status && ['submitted','detected','fulfillment_pending'].includes(status.state);
  const invalidated = () => { ++generation; quote = null; account = null; error = '钱包账户或网络已变化，请重新连接核对原报价。'; if (live()) render(); };
  provider?.on?.('accountsChanged',invalidated); provider?.on?.('chainChanged',invalidated);
  function render() {
    if (!live()) return;
    if (!liveConfig(config) && !locked && !status) { element.hidden = true; return; }
    element.hidden = false;
    const expiry = quote ? Math.max(0, Number(quote.invoice.deadline)-Math.floor(Date.now()/1000)) : 0;
    const stopped = status && ['delivered','review_required'].includes(status.state);
    const saved = readSaved();
    element.innerHTML = `<h2>OKB 钱包付款</h2><p class="muted">X Layer · Chain ID 196 · 原生 OKB，直接支付给商家。</p>${status ? `<p class="notice" role="status">${e(stateLabels[status.state] || '正在核验付款状态')}</p>` : ''}${!liveConfig(config) ? '<p class="notice">真实钱包付款暂未开放；已有交易仍可查询。</p>' : ''}${quote ? `<p>精确应付金额</p><div class="amount">${e(exactOKB(quote.invoice.amountWei)).replace(/(.{10})/g,'$1<wbr>')} <small>OKB</small></div><p class="muted">原订单 ${e(order.total_amount)} USDT · OKX OKB-USDT last</p><p>付款账户</p><pre class="code">${e(quote.invoice.payer)}</pre><p>商家收款地址</p><pre class="code">${e(quote.invoice.merchant)}</pre><details><summary>支付合约和汇率</summary><pre class="code">${e(quote.checkout_address)}</pre><p>1 OKB = ${e(quote.fx_snapshot.rateUSDTPerOKB)} USDT · 无加价</p></details><p class="muted">报价剩余 <span data-tc-seconds>${expiry}</span> 秒；另需网络 Gas。</p>` : ''}${hash || status?.transaction_hash || saved ? `<p>已记录交易，请勿重复付款</p><pre class="code">${e(hash || status?.transaction_hash || saved?.transaction_hash || '发送结果待核查，请打开钱包查看交易记录')}</pre>` : ''}<div class="row">${liveConfig(config) && !activeState() && !stopped && !hash && !saved ? quote && expiry > 0 ? `<button type="button" data-tc="pay" ${busy ? 'disabled' : ''}>确认上述金额并支付 OKB</button>` : `<button type="button" data-tc="connect" ${busy ? 'disabled' : ''}>连接钱包并核对报价</button>` : ''}${locked || status || saved ? `<button class="secondary" type="button" data-tc="refresh" ${busy ? 'disabled' : ''}>查询付款与交付</button>` : ''}</div>${quote && expiry === 0 ? '<p class="notice">报价已过期。请先查询交易；未付款时返回商品页重新创建订单。</p>' : ''}${locked && !activeState() && !stopped ? '<details><summary>已经在钱包发送？提交交易哈希核查</summary><form data-tc-recovery><label class="field">X Layer 交易哈希<input name="hash" pattern="0x[0-9a-fA-F]{64}" required autocomplete="off"></label><button type="submit">提交核验</button></form></details>' : ''}${busy ? '<p class="muted" role="status">正在处理，请勿重复操作…</p>' : ''}${error ? `<p class="error" role="alert">${e(error)}</p>` : ''}`;
  }
  function schedule() { clearTimeout(timer); if (live() && activeState()) timer = setTimeout(() => run(refresh),3000); }
  async function refresh() {
    const result = await request(`${base}/status`);
    if (!live()) return;
    if (!result || !HASH.test(result.invoice_id) || !stateLabels[result.state]) throw Error('付款状态格式错误。');
    status = result; lock();
    if (result.state === 'delivered') { try { storage?.removeItem(storageKey); } catch {} onOrderChanged(); }
    schedule();
  }
  async function submit(transactionHash, invoiceId) {
    if (!HASH.test(transactionHash) || !HASH.test(invoiceId)) throw Error('交易哈希或原报价无效。');
    remember({invoice_id:invoiceId,transaction_hash:transactionHash}); hash = transactionHash; lock();
    await request(`${base}/submit`,{method:'POST',body:{invoice_id:invoiceId,transaction_hash:transactionHash}});
    if (live()) await refresh();
  }
  async function connect() {
    if (!liveConfig(config)) throw Error('真实钱包付款暂未开放。');
    if (!provider?.request) throw Error('请在支持 EIP-1193 的浏览器钱包中打开此页面。');
    // Switching may emit chainChanged, so establish the generation after switching.
    await provider.request({method:'eth_requestAccounts'});
    if (!live()) return;
    if (Number(await provider.request({method:'eth_chainId'})) !== 196) await provider.request({method:'wallet_switchEthereumChain',params:[{chainId:'0xc4'}]});
    if (!live()) return;
    const revision = generation;
    const accounts = await provider.request({method:'eth_accounts'}), chain = await provider.request({method:'eth_chainId'});
    if (!live() || revision !== generation) return;
    if (!address(accounts?.[0]) || Number(chain) !== 196) throw Error('请使用 X Layer 网络和有效钱包账户。');
    account = accounts[0]; lock();
    const result = await request(`${base}/quote`,{method:'POST',body:{payer:account}});
    if (!live() || revision !== generation) return;
    quote = validateQuote(result,config,account,order.total_amount); status = {invoice_id:quote.invoice_id,state:'quote_issued'};
  }
  async function pay() {
    const accepted = quote, revision = generation;
    if (!accepted || hash || readSaved()) throw Error('请先核对已有交易状态。');
    const accounts = await provider.request({method:'eth_accounts'}), chain = await provider.request({method:'eth_chainId'});
    const latestConfig = await request('/public/tapecommerce/config');
    const finalAccounts = await provider.request({method:'eth_accounts'}), finalChain = await provider.request({method:'eth_chainId'});
    if (!live() || revision !== generation) return;
    if (Number(chain) !== 196 || Number(finalChain) !== 196 || !same(accounts?.[0],accepted.invoice.payer) || !same(finalAccounts?.[0],accepted.invoice.payer)) { invalidated(); return; }
    validateQuote(accepted,latestConfig,accounts[0],order.total_amount);
    // Mark the uncertain send before opening the wallet: never offer another send
    // until the buyer deliberately recovers or reloads the authoritative order.
    lock(); remember({invoice_id:accepted.invoice_id,uncertain:true},true); hash = '等待钱包返回交易哈希'; render();
    let transactionHash;
    try { transactionHash = await provider.request({method:'eth_sendTransaction',params:[{chainId:'0xc4',from:accounts[0],to:accepted.checkout_address,value:'0x'+BigInt(accepted.amount_wei).toString(16),data:encodePayOKB(accepted.invoice,accepted.signature)}]}); }
    catch (failure) { if (failure?.code === 4001) { hash = null; try { storage?.removeItem(storageKey); } catch {} } throw failure; }
    if (!HASH.test(transactionHash)) throw Error('钱包未返回有效交易哈希。请从钱包交易记录核查，勿重复付款。');
    // Keep a broadcast even after navigation/account change, but do not mutate a stale UI.
    remember({invoice_id:accepted.invoice_id,transaction_hash:transactionHash}); hash = transactionHash;
    await request(`${base}/submit`,{method:'POST',body:{invoice_id:accepted.invoice_id,transaction_hash:transactionHash}});
    if (live()) await refresh();
  }
  async function run(action) {
    if (!live() || busy) return;
    busy = true; error = ''; render();
    try { await action(); } catch (failure) {
      if (live()) error = /tapecommerce_quote_expired_new_order/.test(failure?.message) ? '原报价已过期。请先核查是否已发送交易；未付款时重新创建订单。' : failure?.code === 4001 ? '钱包操作已取消，尚未确认付款。' : failure?.message || '连接失败，请查询原订单。';
    } finally { busy = false; if (live()) { render(); schedule(); } }
  }
  const click = event => { const target = event.target.closest('[data-tc]'); if (!target || target.disabled) return; if (target.dataset.tc === 'connect') run(connect); if (target.dataset.tc === 'pay') run(pay); if (target.dataset.tc === 'refresh') run(async () => { const saved = readSaved(); await refresh(); if (saved?.transaction_hash && status?.state === 'quote_issued') await submit(saved.transaction_hash,saved.invoice_id); }); };
  const recover = event => { if (!event.target.matches('[data-tc-recovery]')) return; event.preventDefault(); const value = new FormData(event.target).get('hash'); run(async () => { if (!status?.invoice_id) await refresh(); await submit(value,status.invoice_id); }); };
  element.addEventListener('click',click); element.addEventListener('submit',recover);
  tick = setInterval(() => { if (!live() || !quote || busy) return; const left = Math.max(0,Number(quote.invoice.deadline)-Math.floor(Date.now()/1000)); const count = element.querySelector('[data-tc-seconds]'); if (count) count.textContent = String(left); if (!left && element.querySelector('[data-tc="pay"]')) render(); },1000);
  void run(async () => { try { config = await request('/public/tapecommerce/config'); } catch (failure) { if (!existing && !readSaved()) throw failure; } if (!live()) return; try { await refresh(); } catch (failure) { if (failure.statusCode !== 404 || existing || readSaved()) throw failure; } });
  return () => { disposed = true; ++generation; clearTimeout(timer); clearInterval(tick); element.removeEventListener('click',click); element.removeEventListener('submit',recover); provider?.removeListener?.('accountsChanged',invalidated); provider?.removeListener?.('chainChanged',invalidated); };
}
