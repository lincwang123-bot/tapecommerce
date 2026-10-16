import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {encodePayOKB, exactOKB, liveConfig, validateQuote, mountTapeCommerceCheckout} from './tapecommerce-checkout.js';
const address = n => '0x' + n.repeat(40), hash = n => '0x'+n.repeat(64);
const config = {mode:'live',enabled:true,available:true,ready:true,chain_id:196,native_symbol:'OKB',checkout_address:address('1')};
const types = {Invoice:[['invoiceId','bytes32'],['tenantHash','bytes32'],['payer','address'],['merchant','address'],['amountWei','uint256'],['deadline','uint64']].map(([name,type])=>({name,type}))};
const makeQuote = () => { const invoice = {invoiceId:hash('2'),tenantHash:hash('3'),payer:address('4'),merchant:address('5'),amountWei:'3333333333333334',deadline:String(Math.floor(Date.now()/1000)+60)}; return {invoice_id:invoice.invoiceId,amount_wei:invoice.amountWei,deadline:invoice.deadline,chain_id:196,checkout_address:config.checkout_address,domain:{name:'TapeCommerceCheckout',version:'1',chainId:196,verifyingContract:config.checkout_address},types,invoice,signature:'0x'+'a'.repeat(128)+'1b',fx_snapshot:{amountUSDT:'0.01',rateUSDTPerOKB:'3'}}; };
const copy = value => JSON.parse(JSON.stringify(value));
const flush = async () => { for(let i=0;i<30;i++) await Promise.resolve(); };
function fixture(t,{ready=true,initial,send,requestHook,saved,storageDenied=false}={}) {
  const listeners = new Map(), walletListeners = new Map(), values = new Map(), calls = [], requests = [];
  if(saved)values.set('tapecommerce-tx:ORDER-1',JSON.stringify(saved));
  const element = {isConnected:true,hidden:false,innerHTML:'',addEventListener:(name,fn)=>listeners.set(name,fn),removeEventListener:name=>listeners.delete(name),querySelector:()=>null};
  let current = true, result = initial, sent = 0, locks = 0, changes = 0, accounts=[address('4')], chain='0xc4';
  const provider = {on:(name,fn)=>walletListeners.set(name,fn),removeListener:name=>walletListeners.delete(name),request:async args => {calls.push(args);if(args.method==='eth_accounts'||args.method==='eth_requestAccounts')return accounts;if(args.method==='eth_chainId')return chain;if(args.method==='wallet_switchEthereumChain'){chain='0xc4';walletListeners.get('chainChanged')?.(chain);return;}if(args.method==='eth_sendTransaction'){sent++;return send ? send(args) : hash('6');}throw Error(args.method);}};
  const storage = {getItem:key=>values.get(key),setItem:(key,value)=>{if(storageDenied)throw Error('denied');values.set(key,value);},removeItem:key=>values.delete(key)};
  const request = async(path,options={})=>{requests.push([path,options]);if(requestHook){const response=await requestHook(path,options);if(response!==undefined)return response;}if(path==='/public/tapecommerce/config')return {...config,ready};if(path.endsWith('/quote'))return makeQuote();if(path.endsWith('/submit')){result={invoice_id:options.body.invoice_id,state:'submitted',transaction_hash:options.body.transaction_hash};return result;}if(path.endsWith('/status')){if(!result)throw Object.assign(Error('not found'),{statusCode:404});return result;}throw Error(path);};
  const stop = mountTapeCommerceCheckout(element,{order:{order_no:'ORDER-1',total_amount:'0.01'},request,provider,storage,isCurrent:()=>current,onPaymentLock:()=>locks++,onOrderChanged:()=>changes++});t.after(stop);
  const click = async action=>{listeners.get('click')?.({target:{closest:()=>({dataset:{tc:action}})}});await flush();};
  return {element,calls,requests,values,walletListeners,stop,click,provider,setStatus:value=>{result=value;},setCurrent:value=>{current=value;},changeAccount:value=>{accounts=[value];walletListeners.get('accountsChanged')?.(accounts);},silentAccount:value=>{accounts=[value];},get sent(){return sent;},get locks(){return locks;},get changes(){return changes;}};
}
test('real readiness requires all server capabilities and exact chain/native asset',()=>{
  assert.ok(liveConfig(config));for(const patch of [{ready:false},{available:false},{enabled:false},{mode:'demo'},{chain_id:1},{native_symbol:'USDT'},{checkout_address:address('0')}])assert.equal(liveConfig({...config,...patch}),false);
});
test('display retains every wei and rejects imprecise/malformed numeric inputs',()=>{
  assert.equal(exactOKB('3333333333333334'),'0.003333333333333334');assert.equal(exactOKB('1000000000000000000'),'1');assert.equal(exactOKB('1'),'0.000000000000000001');for(const value of [1,'01','0','1e18','-1',(2n**256n).toString()])assert.throws(()=>exactOKB(value));
});
test('quote binds domain, chain, payer, merchant, invoice, exact order amount, expiry and ABI fields',()=>{
  const original=makeQuote();assert.equal(validateQuote(original,config,address('4'),'0.01'),original);
  const mutations=[q=>q.domain.chainId=1,q=>q.domain.name='Wrong',q=>q.domain.version='2',q=>q.domain.verifyingContract=address('6'),q=>q.chain_id=1,q=>q.checkout_address=address('6'),q=>q.invoice.payer=address('6'),q=>q.invoice.merchant=address('0'),q=>q.invoice.merchant=config.checkout_address,q=>q.invoice.invoiceId=hash('0'),q=>q.invoice.tenantHash=hash('0'),q=>q.invoice_id=hash('6'),q=>q.amount_wei='2',q=>q.deadline='1',q=>q.signature='0x1234',q=>q.types.Invoice.reverse(),q=>q.fx_snapshot.amountUSDT='0.02',q=>{q.invoice.deadline='1';q.deadline='1';}];
  for(const mutate of mutations){const q=copy(original);mutate(q);assert.throws(()=>validateQuote(q,config,address('4'),'0.01'));}
});
test('fixed ABI calldata matches an independent ethers-generated fixture',()=>{
  const fixture=JSON.parse(readFileSync(new URL('./testdata/tapecommerce-abi.json',import.meta.url),'utf8'));
  assert.equal(encodePayOKB(fixture.invoice,fixture.signature).toLowerCase(),fixture.calldata.toLowerCase());
});
test('unready config cannot expose wallet controls or request accounts',async t=>{
  const f=fixture(t,{ready:false});await flush();assert.equal(f.element.hidden,true);await f.click('connect');assert.equal(f.sent,0);assert.equal(f.calls.length,0);
});
test('wallet sends exact value to the configured contract and waits for server delivery',async t=>{
  const f=fixture(t);await flush();await f.click('connect');assert.match(f.element.innerHTML.replaceAll('<wbr>',''),/0.003333333333333334/);assert.match(f.element.innerHTML,new RegExp(address('5')));await f.click('pay');assert.equal(f.sent,1);const transaction=f.calls.find(c=>c.method==='eth_sendTransaction').params[0];assert.equal(transaction.value,'0xbd7a625405556');assert.equal(transaction.chainId,'0xc4');assert.equal(transaction.to,config.checkout_address);assert.ok(transaction.data.startsWith('0xc14fb694'));assert.equal(f.changes,0);assert.ok(f.requests.some(([path,args])=>path.endsWith('/submit')&&args.body.transaction_hash===hash('6')));f.setStatus({invoice_id:hash('2'),state:'delivered'});await f.click('refresh');assert.equal(f.changes,1);
});
test('account changes invalidate the displayed invoice and prevent sending',async t=>{
  const f=fixture(t);await flush();await f.click('connect');f.changeAccount(address('7'));await f.click('pay');assert.equal(f.sent,0);assert.doesNotMatch(f.element.innerHTML,/data-tc="pay"/);
});
test('late quote cannot overwrite a changed wallet account',async t=>{
  let release;const f=fixture(t,{requestHook:path=>path.endsWith('/quote')?new Promise(resolve=>release=resolve):undefined});await flush();await f.click('connect');f.changeAccount(address('7'));release(makeQuote());await flush();assert.doesNotMatch(f.element.innerHTML,/data-tc="pay"/);assert.equal(f.sent,0);
});
test('uncertain send persists a recovery marker and never offers a second send',async t=>{
  const f=fixture(t,{send:async()=>{throw Error('RPC timeout');}});await flush();await f.click('connect');await f.click('pay');assert.equal(f.sent,1);assert.equal(JSON.parse([...f.values.values()][0]).uncertain,true);await f.click('pay');assert.equal(f.sent,1);assert.doesNotMatch(f.element.innerHTML,/data-tc="pay"/);
});
test('explicit wallet rejection clears the uncertainty marker without submitting',async t=>{
  const f=fixture(t,{send:async()=>{throw Object.assign(Error('rejected'),{code:4001});}});await flush();await f.click('connect');await f.click('pay');assert.equal(f.values.size,0);assert.equal(f.requests.some(([p])=>p.endsWith('/submit')),false);
});
test('broadcast returned after navigation is recorded and submitted without updating stale DOM',async t=>{
  let release;const f=fixture(t,{send:()=>new Promise(resolve=>release=resolve)});await flush();await f.click('connect');await f.click('pay');f.setCurrent(false);const previous=f.element.innerHTML;f.stop();release(hash('6'));await flush();assert.equal(JSON.parse([...f.values.values()][0]).transaction_hash,hash('6'));assert.ok(f.requests.some(([path])=>path.endsWith('/submit')));assert.equal(f.element.innerHTML,previous);assert.equal(f.walletListeners.size,0);
});
test('a pre-existing invoice locks ordinary payment before offering recovery',async t=>{
  const f=fixture(t,{initial:{invoice_id:hash('2'),state:'submitted',transaction_hash:hash('6')}});await flush();assert.ok(f.locks>0);assert.doesNotMatch(f.element.innerHTML,/data-tc="connect"/);assert.equal(f.calls.length,0);
});

test('reload with an uncertain send marker preserves recovery and cannot create another payment',async t=>{
  const f=fixture(t,{initial:{invoice_id:hash('2'),state:'quote_issued'},saved:{invoice_id:hash('2'),uncertain:true}});await flush();assert.doesNotMatch(f.element.innerHTML,/data-tc="(?:pay|connect)"/);assert.match(f.element.innerHTML,/发送结果待核查/);await f.click('pay');assert.equal(f.sent,0);
});
test('blocked session storage stops before opening the wallet transaction confirmation',async t=>{
  const f=fixture(t,{storageDenied:true});await flush();await f.click('connect');await f.click('pay');assert.equal(f.sent,0);assert.match(f.element.innerHTML,/无法保存付款恢复记录/);
});
test('silent account change during config refresh is checked again before transaction send',async t=>{
  let configReads=0,f;f=fixture(t,{requestHook:path=>{if(path==='/public/tapecommerce/config'&&++configReads===2)f.silentAccount(address('7'));}});await flush();await f.click('connect');await f.click('pay');assert.equal(f.sent,0);assert.match(f.element.innerHTML,/钱包账户或网络已变化/);
});
test('disabled live config still displays an existing payment and never opens a new wallet send',async t=>{
  const f=fixture(t,{ready:false,initial:{invoice_id:hash('2'),state:'submitted',transaction_hash:hash('6')}});await flush();assert.equal(f.element.hidden,false);assert.match(f.element.innerHTML,/交易已提交/);assert.doesNotMatch(f.element.innerHTML,/data-tc="(?:pay|connect)"/);assert.equal(f.calls.length,0);
});
