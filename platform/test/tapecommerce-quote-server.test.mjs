// SPDX-License-Identifier: GPL-3.0-only
import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdtemp, writeFile, chmod, symlink, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Wallet, Interface, keccak256, verifyTypedData } from 'ethers';
import { createQuoteHandler, createQuoteService, CHECKOUT_READ_ABI } from '../src/tapecommerce/quote-server.mjs';
import { loadQuoteConfig, validateQuoteConfig, readStudioBinding } from '../src/tapecommerce/quote-config.mjs';

const now = 1792167000000;
function fixture() {
  const signer = Wallet.createRandom(), merchant = Wallet.createRandom().address, checkout = Wallet.createRandom().address;
  const iface = new Interface(CHECKOUT_READ_ABI), code = '0x6001600055';
  const state = { holder: merchant, live: true, signer: signer.address, chain: 196n, code, changedBlock: false, revoked: false, stalled: false, domainVersion:'1', finality:'safe' };
  const config = { enabled: true, tenant: 'shop-test', merchant, checkout, quoteSigner: signer.address, signer,
    checkoutCodeHash: keccak256(code), processorNumber: '0', tokenId: '1', rpcUrl: 'https://rpc.xlayer.tech',
    bindingDb: '/unused-test-binding.db', authSecret: 'synthetic-secret-for-tests-at-least-32-characters', timeoutMs: 1000, maxConcurrent: 2 };
  const rpc = async (method, params) => {
    if (state.stalled) return new Promise(() => {});
    if (method === 'eth_chainId') return '0xc4';
    if (method === 'eth_getCode') return state.code;
    if (method === 'eth_getBlockByNumber') return { number: '0x2a', hash: '0x' + (state.changedBlock ? '22' : '11').repeat(32) };
    if (method === 'eth_call') {
      const call = iface.parseTransaction({ data: params[0].data });
      const values = { CHAIN_ID: [state.chain], quoteSigner: [state.signer], eip712Domain: ['0x0f', 'TapeCommerceCheckout', state.domainVersion, 196n, checkout, '0x'+'00'.repeat(32), []] };
      return iface.encodeFunctionResult(call.name, values[call.name]);
    }
    throw new Error('unexpected RPC');
  };
  const ports = { now: () => now, rpcFactory: () => rpc,
    lookupIdentity: async ({ processorNumber, tokenId }) => { assert.equal(processorNumber, '0'); assert.equal(tokenId, '1'); return { chainId: 196, finality:state.finality, holder: state.holder, opened: true, live: state.live, blockNumber: 42, blockHash: '0x'+'11'.repeat(32) }; },
    readBinding: async () => state.revoked ? null : ({ wallet: merchant, revision: 'ab'.repeat(24), verified_at: now - 1000, revoked_at: null }),
    rates: { latest: async () => ({ source: 'okx', instrumentId: 'OKB-USDT', priceField: 'last', rateUSDTPerOKB: '100', sourceTimestampMs: now, fetchedAtMs: now }) } };
  const input = { orderId: 7, tenant: config.tenant, buyer: 'user:12', currency: 'USDT', amountUSDT: '1.00', status: 'pending', payer: Wallet.createRandom().address, expiresAtMs: now + 300000 };
  return { config, ports, state, input, service: createQuoteService(config, ports) };
}
test('private quote pins current identity, contract and signer; exact EIP712 output', async () => {
  const f=fixture(), health=await f.service.health(new AbortController().signal);
  assert.deepEqual(health,{ready:true,mode:'live',chainId:196,tenant:f.config.tenant,checkout:f.config.checkout,merchant:f.config.merchant,quoteSigner:f.config.quoteSigner});
  const q=await f.service.quote(f.input,new AbortController().signal);
  assert.equal(q.invoice.amountWei,'10000000000000000');
  assert.equal(q.invoice.merchant,f.config.merchant);
  assert.equal(q.invoice.payer,f.input.payer);
  assert.equal(verifyTypedData(q.domain,q.types,q.invoice,q.signature),f.config.quoteSigner);
});
test('wrong scope and caller-selected security fields are rejected before signing', async t => {
  const changes=[{tenant:'other'},{merchant:Wallet.createRandom().address},{checkout:'0x'+'12'.repeat(20)},{signer:'ignored'},
    {amountUSDT:1},{amountUSDT:'1.0'},{amountUSDT:'-1.00'},{currency:'OKB'},{status:'paid'},{buyer:'admin:12'},
    {orderId:Number.MAX_SAFE_INTEGER+1},{expiresAtMs:now+59000},{payer:'0x'+'00'.repeat(20)}];
  for(const change of changes) await t.test(Object.keys(change).join(','),async()=>{
    const f=fixture();await assert.rejects(f.service.quote({...f.input,...change},new AbortController().signal));
  });
});
test('identity, binding and immutable contract mismatches fail closed', async t => {
  for(const [name,change] of Object.entries({holder:{holder:Wallet.createRandom().address},notLive:{live:false},latest:{finality:'latest'},badSigner:{signer:Wallet.createRandom().address},badChain:{chain:1n},badCode:{code:'0x6000'},badDomain:{domainVersion:'2'},reorg:{changedBlock:true},revoked:{revoked:true}})) {
    await t.test(name,async()=>{const f=fixture();Object.assign(f.state,change);await assert.rejects(f.service.health(new AbortController().signal));});
  }
});
test('binding revoked during FX fetch prevents signature exposure',async()=>{
  const f=fixture();f.ports.rates.latest=async()=>{f.state.revoked=true;return {source:'okx',instrumentId:'OKB-USDT',priceField:'last',rateUSDTPerOKB:'100',sourceTimestampMs:now,fetchedAtMs:now};};
  await assert.rejects(f.service.quote(f.input,new AbortController().signal));
});
test('private HTTP auth, Origin, body, method and timeout boundaries',async t=>{
  const f=fixture();f.config.timeoutMs=50;
  const server=createServer(createQuoteHandler(f.config,f.ports));
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  t.after(()=>new Promise(resolve=>server.close(resolve)));
  const base=`http://127.0.0.1:${server.address().port}/internal/tapecommerce`;
  const auth={authorization:`Bearer ${f.config.authSecret}`};
  assert.equal((await fetch(base+'/health')).status,401);
  assert.equal((await fetch(base+'/health',{headers:{...auth,origin:'https://attacker.example'}})).status,403);
  assert.equal((await fetch(base+'/health',{headers:auth})).status,200);
  assert.equal((await fetch(base+'/quote',{headers:auth})).status,405);
  assert.equal((await fetch(base+'/quote?merchant=x',{headers:auth})).status,404);
  let response=await fetch(base+'/quote',{method:'POST',headers:{...auth,'content-type':'application/json'},body:JSON.stringify(f.input)});
  assert.equal(response.status,200);assert.ok((await response.json()).signature);
  response=await fetch(base+'/quote',{method:'POST',headers:{...auth,'content-type':'application/json'},body:'x'.repeat(8193)});
  assert.equal(response.status,413);
  f.state.stalled=true;
  response=await fetch(base+'/health',{headers:auth});
  assert.equal(response.status,503);assert.deepEqual(await response.json(),{error:'quote_service_timeout'});
});
test('disabled service never resolves identity or signs',async()=>{
  const f=fixture();f.config.enabled=false;
  const service=createQuoteService(f.config,{lookupIdentity:()=>{throw new Error('must not run');}});
  await assert.rejects(service.health(new AbortController().signal),{code:'quote_service_disabled'});
});
test('private HTTP concurrency cap recovers after the admitted request completes',async t=>{
  const f=fixture();f.config.maxConcurrent=1;
  let enter,release;const entered=new Promise(resolve=>{enter=resolve;}),gate=new Promise(resolve=>{release=resolve;});
  const original=f.ports.lookupIdentity;
  f.ports.lookupIdentity=async args=>{enter();await gate;return original(args);};
  const server=createServer(createQuoteHandler(f.config,f.ports));
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  t.after(()=>new Promise(resolve=>{release();server.close(resolve);}));
  const url=`http://127.0.0.1:${server.address().port}/internal/tapecommerce/health`,headers={authorization:`Bearer ${f.config.authSecret}`};
  const first=fetch(url,{headers});await entered;
  const second=await fetch(url,{headers});assert.equal(second.status,503);assert.deepEqual(await second.json(),{error:'quote_busy'});
  release();assert.equal((await first).status,200);
  assert.equal((await fetch(url,{headers})).status,200);
});
test('private HTTP timeout aborts the in-flight upstream fetch and releases capacity',async t=>{
  const f=fixture();f.config.timeoutMs=40;f.config.maxConcurrent=1;delete f.ports.rpcFactory;
  let observed,aborts=0;
  f.ports.fetchImpl=async(_url,options)=>{observed=options.signal;return new Promise((_,reject)=>{
    options.signal.addEventListener('abort',()=>{aborts++;reject(options.signal.reason);},{once:true});
  });};
  const server=createServer(createQuoteHandler(f.config,f.ports));
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  t.after(()=>new Promise(resolve=>server.close(resolve)));
  const url=`http://127.0.0.1:${server.address().port}/internal/tapecommerce/health`,headers={authorization:`Bearer ${f.config.authSecret}`};
  for(let i=0;i<2;i++){
    const response=await fetch(url,{headers});assert.equal(response.status,503);
    assert.deepEqual(await response.json(),{error:'quote_service_timeout'});assert.equal(observed.aborted,true);
  }
  assert.equal(aborts,2);
});
test('incorrect signer implementation cannot return an unusable signature',async()=>{
  const f=fixture(),other=Wallet.createRandom();
  f.config.signer={address:f.config.quoteSigner,signTypedData:(...args)=>other.signTypedData(...args)};
  await assert.rejects(createQuoteService(f.config,f.ports).quote(f.input,new AbortController().signal),{code:'quote_signature_invalid'});
});
test('RPC redirects are rejected and request cancellation reaches fetch',async()=>{
  const f=fixture();delete f.ports.rpcFactory;
  let observed;
  f.ports.fetchImpl=async(_url,options)=>{observed=options;const response=new Response('{}',{status:200});Object.defineProperty(response,'redirected',{value:true});return response;};
  await assert.rejects(createQuoteService(f.config,f.ports).health(new AbortController().signal),{code:'quote_upstream_unavailable'});
  assert.equal(observed.redirect,'error');assert.equal(observed.credentials,'omit');
  const controller=new AbortController();controller.abort();
  await assert.rejects(createQuoteService(f.config,f.ports).health(controller.signal),{code:'quote_service_timeout'});
});
test('service rejects exchange timestamp regression across separate quote requests',async()=>{
  const f=fixture();await f.service.quote(f.input,new AbortController().signal);
  f.ports.rates.latest=async()=>({source:'okx',instrumentId:'OKB-USDT',priceField:'last',rateUSDTPerOKB:'100',sourceTimestampMs:now-1,fetchedAtMs:now});
  await assert.rejects(f.service.quote({...f.input,orderId:8},new AbortController().signal),{code:'fx_rate_regressed'});
});
test('explicit config and secret-file permissions fail closed; Studio binding is read-only',async t=>{
  const f=fixture(),dir=await mkdtemp(path.join(os.tmpdir(),'tape-quote-config-test-'));
  t.after(()=>rm(dir,{recursive:true,force:true}));
  const auth=path.join(dir,'auth'),key=path.join(dir,'key'),dbpath=path.join(dir,'studio.db');
  await writeFile(auth,f.config.authSecret,{mode:0o600});await writeFile(key,f.config.signer.privateKey,{mode:0o600});
  const env={TAPE_QUOTE_ENABLED:'true',TAPE_QUOTE_TENANT:f.config.tenant,TAPE_QUOTE_MERCHANT:f.config.merchant,
    TAPE_QUOTE_PROCESSOR_NUMBER:'0',TAPE_QUOTE_TOKEN_ID:'1',TAPE_QUOTE_CHECKOUT:f.config.checkout,TAPE_QUOTE_CHECKOUT_CODE_HASH:f.config.checkoutCodeHash,
    TAPE_QUOTE_SIGNER_ADDRESS:f.config.quoteSigner,TAPE_QUOTE_RPC_URL:'https://rpc.xlayer.tech',TAPE_QUOTE_BINDING_DB:dbpath,
    TAPE_QUOTE_AUTH_SECRET_FILE:auth,TAPE_QUOTE_SIGNER_KEY_FILE:key};
  assert.equal((await loadQuoteConfig({})).enabled,false);
  assert.equal((await loadQuoteConfig(env)).quoteSigner,f.config.quoteSigner);
  await chmod(key,0o644);await assert.rejects(loadQuoteConfig(env),{code:'quote_secret_file_invalid'});await chmod(key,0o600);
  const link=path.join(dir,'link');await symlink(key,link);await assert.rejects(loadQuoteConfig({...env,TAPE_QUOTE_SIGNER_KEY_FILE:link}),{code:'quote_secret_file_invalid'});
  for(const patch of [{host:'0.0.0.0'},{checkoutCodeHash:''},{quoteSigner:Wallet.createRandom().address},{rpcUrl:'http://rpc.xlayer.tech'},{tokenId:'0'},{authSecret:'short'}]) assert.throws(()=>validateQuoteConfig({...f.config,...patch}));
  const {DatabaseSync}=await import('node:sqlite');const db=new DatabaseSync(dbpath);
  db.exec('CREATE TABLE tape_wallet_bindings(shop_id TEXT PRIMARY KEY,wallet TEXT,verified_at INTEGER,revision TEXT,revoked_at INTEGER)');
  db.prepare('INSERT INTO tape_wallet_bindings VALUES(?,?,?,?,NULL)').run(f.config.tenant,f.config.merchant,now-1000,'ab'.repeat(24));db.close();
  const binding=await readStudioBinding({...f.config,bindingDb:dbpath});assert.equal(binding.wallet,f.config.merchant);assert.equal(binding.revoked_at,null);
});
