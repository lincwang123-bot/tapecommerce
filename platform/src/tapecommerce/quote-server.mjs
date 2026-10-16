// SPDX-License-Identifier: GPL-3.0-only
import { createHash, timingSafeEqual } from 'node:crypto';
import { Interface, getAddress, keccak256, verifyTypedData } from 'ethers';
import { signTrustedOrder } from './quote.mjs';
import { QUOTE_TTL_SECONDS, usdtToOKBWei, validateRate } from './fx.mjs';
import { createOkxSpotRates } from './okx-rate.mjs';
import { fail } from './errors.mjs';
import { validateQuoteConfig, readStudioBinding } from './quote-config.mjs';
import { createReadonlyRpc, readIdentity, verifyPinnedBlock } from '../../../scripts/tapecommerce/readonly-adapter.mjs';

export const CHECKOUT_READ_ABI=Object.freeze(['function CHAIN_ID() view returns (uint256)','function quoteSigner() view returns (address)',
  'function eip712Domain() view returns (bytes1,string,string,uint256,address,bytes32,uint256[])']);
const abi=new Interface(CHECKOUT_READ_ABI), bodyKeys=['orderId','tenant','buyer','currency','amountUSDT','status','payer','expiresAtMs'];
const sameAddress=(a,b)=>typeof a==='string'&&typeof b==='string'&&a.toLowerCase()===b.toLowerCase();
const digest=value=>createHash('sha256').update(value).digest();
const alive=signal=>{if(signal?.aborted)fail('quote_service_timeout',503);};
function inputOrder(value,config,now) {
  if(!value||typeof value!=='object'||Array.isArray(value)||Object.keys(value).length!==bodyKeys.length||bodyKeys.some(k=>!Object.hasOwn(value,k))) fail('quote_invalid_request');
  if(value.tenant!==config.tenant)fail('quote_wrong_tenant',403);
  if(!Number.isSafeInteger(value.orderId)||value.orderId<=0||typeof value.buyer!=='string'||!/^(user|guest):[1-9][0-9]{0,19}$/.test(value.buyer)||value.currency!=='USDT'||value.status!=='pending')fail('quote_invalid_request');
  if(value.buyer.startsWith('guest:')&&value.buyer!==`guest:${value.orderId}`)fail('quote_invalid_request');
  if(!Number.isSafeInteger(value.expiresAtMs)||value.expiresAtMs<=(Math.floor(now/1000)+QUOTE_TTL_SECONDS)*1000)fail('quote_order_expiring',409);
  let payer;try{payer=getAddress(value.payer);}catch{fail('quote_invalid_request');}
  if(/^0x0{40}$/i.test(payer))fail('quote_invalid_request');
  usdtToOKBWei(value.amountUSDT,'1');
  return Object.freeze({...value,payer,id:value.orderId});
}
// Redirects and unbounded RPC bodies are rejected before the shared adapter can
// parse them. Signals cover all downstream reads when the private request ends.
function guardedFetch(fetchImpl,signal) {
  return async (url,options={})=>{
    alive(signal);
    const response=await fetchImpl(url,{...options,redirect:'error',credentials:'omit',signal:AbortSignal.any([signal,options.signal].filter(Boolean))});
    if(response.status!==200||response.redirected||(response.url&&response.url!==String(url)))fail('quote_upstream_unavailable',503);
    const chunks=[];let size=0;
    for await(const chunk of response.body??[]){size+=chunk.byteLength;if(size>131072){await response.body?.cancel?.().catch(()=>{});fail('quote_upstream_unavailable',503);}chunks.push(chunk);}
    return new Response(Buffer.concat(chunks),{status:response.status,headers:response.headers});
  };
}
export function createQuoteService(input,ports={}) {
  const config=validateQuoteConfig(input),now=ports.now??Date.now,readBinding=ports.readBinding??readStudioBinding;
  let newestRateTimestamp=0;
  const binding=async()=>{
    const b=await readBinding(config);
    if(!b||b.revoked_at!==null||!sameAddress(b.wallet,config.merchant)||!Number.isSafeInteger(b.verified_at)||b.verified_at<=0||b.verified_at>now()||typeof b.revision!=='string'||!/^[a-f0-9]{48}$/.test(b.revision))fail('quote_wallet_binding_required',503);
    return b;
  };
  const preflight=async(signal)=>{
    if(!config.enabled)fail('quote_service_disabled',503);
    alive(signal);const bound=await binding();
    const fetchImpl=guardedFetch(ports.fetchImpl??globalThis.fetch,signal);
    const rpc=ports.rpcFactory?ports.rpcFactory(config,signal):createReadonlyRpc({rpcUrl:config.rpcUrl,fetchImpl,timeoutMs:Math.min(config.timeoutMs,10000)});
    const identity=await (ports.lookupIdentity??readIdentity)({processorNumber:config.processorNumber,tokenId:config.tokenId,rpc,expectedOwner:config.merchant});
    if(identity?.chainId!==196||identity.finality!=='safe'||identity.opened!==true||identity.live!==true||!sameAddress(identity.holder,config.merchant)||!Number.isSafeInteger(identity.blockNumber)||identity.blockNumber<0||!/^0x[0-9a-f]{64}$/i.test(identity.blockHash))fail('quote_identity_unavailable',503);
    const block={number:'0x'+BigInt(identity.blockNumber).toString(16),hash:identity.blockHash};
    if(BigInt(await rpc('eth_chainId',[]))!==196n)fail('quote_contract_mismatch',503);
    const code=await rpc('eth_getCode',[config.checkout,block.number]);
    if(!/^0x(?:[0-9a-f]{2})+$/i.test(code)||keccak256(code).toLowerCase()!==config.checkoutCodeHash.toLowerCase())fail('quote_contract_mismatch',503);
    const call=async name=>abi.decodeFunctionResult(name,await rpc('eth_call',[{to:config.checkout,data:abi.encodeFunctionData(name,[])},block.number]));
    const [[chain],[signer],domain]=await Promise.all([call('CHAIN_ID'),call('quoteSigner'),call('eip712Domain')]);
    if(chain!==196n||!sameAddress(signer,config.quoteSigner)||domain[0]!=='0x0f'||domain[1]!=='TapeCommerceCheckout'||domain[2]!=='1'||domain[3]!==196n||!sameAddress(domain[4],config.checkout)||domain[5]!=='0x'+'00'.repeat(32)||domain[6].length!==0)fail('quote_contract_mismatch',503);
    await verifyPinnedBlock(rpc,block);alive(signal);
    if((await binding()).revision!==bound.revision)fail('quote_wallet_binding_changed',503);
    return {identity:{...identity,status:'verified'},bound,fetchImpl};
  };
  return Object.freeze({
    async health(signal){await preflight(signal);return {ready:true,mode:'live',chainId:196,tenant:config.tenant,checkout:config.checkout,merchant:config.merchant,quoteSigner:config.quoteSigner};},
    async quote(value,signal){
      if(!config.enabled)fail('quote_service_disabled',503);
      const order=inputOrder(value,config,now());
      const checked=await preflight(signal);
      const signer={async signTypedData(...args){alive(signal);if((await binding()).revision!==checked.bound.revision)fail('quote_wallet_binding_changed',503);return config.signer.signTypedData(...args);}};
      const source=ports.rates??createOkxSpotRates({fetchImpl:checked.fetchImpl,now});
      const rates={async latest(){const rate=validateRate(await source.latest(),now());if(rate.sourceTimestampMs<newestRateTimestamp)fail('fx_rate_regressed',503);newestRateTimestamp=rate.sourceTimestampMs;return rate;}};
      const result=await signTrustedOrder({orderId:order.id,buyer:order.buyer,tenant:config.tenant,
        orders:{loadPayable:async()=>order},identities:{verifyCurrent:async()=>checked.identity},signer,
        checkoutContract:config.checkout,rates,now});
      alive(signal);
      if(Number(result.invoice.deadline)*1000>=order.expiresAtMs)fail('quote_order_expiring',409);
      if((await binding()).revision!==checked.bound.revision)fail('quote_wallet_binding_changed',503);
      if(verifyTypedData(result.domain,result.types,result.invoice,result.signature)!==config.quoteSigner)fail('quote_signature_invalid',503);
      return result;
    }
  });
}
async function body(req){
  if(!/^application\/json(?:\s*;|$)/i.test(req.headers['content-type']??''))fail('quote_content_type',415);
  if(req.headers['content-encoding'])fail('quote_content_encoding',415);
  if(Number(req.headers['content-length']??0)>8192)fail('quote_body_too_large',413);
  const chunks=[];let size=0;
  for await(const part of req){size+=part.length;if(size>8192)fail('quote_body_too_large',413);chunks.push(part);}
  try{return JSON.parse(Buffer.concat(chunks).toString('utf8'));}catch{fail('quote_invalid_json');}
}
export function createQuoteHandler(input,ports={}){
  const config=validateQuoteConfig(input),service=createQuoteService(config,ports);let active=0,windowStart=0,requests=0;
  return async(req,res)=>{
    const controller=new AbortController();let timer,admitted=false;
    const closed=()=>controller.abort();res.once('close',closed);
    const send=(status,value)=>{if(res.destroyed||res.writableEnded)return;res.writeHead(status,{'content-type':'application/json; charset=utf-8','cache-control':'no-store','x-content-type-options':'nosniff','connection':'close'});res.end(JSON.stringify(value));};
    try{
      if(req.headers.origin!==undefined)fail('quote_origin_forbidden',403);
      if(!config.enabled)fail('quote_service_disabled',503);
      const authorization=req.headers.authorization;
      if(typeof authorization!=='string'||authorization.length>300||!timingSafeEqual(digest(authorization),digest(`Bearer ${config.authSecret}`)))fail('quote_unauthorized',401);
      const action=req.url==='/internal/tapecommerce/health'?'health':req.url==='/internal/tapecommerce/quote'?'quote':null;
      if(!action)fail('quote_not_found',404);
      if(req.method!==(action==='health'?'GET':'POST'))fail('quote_method_not_allowed',405);
      const ts=Date.now();if(ts-windowStart>60000){windowStart=ts;requests=0;}if(++requests>120||active>=config.maxConcurrent)fail('quote_busy',503);
      active++;admitted=true;
      const work=(async()=>action==='health'?service.health(controller.signal):service.quote(await body(req),controller.signal))();
      const timeout=new Promise((_,reject)=>{timer=setTimeout(()=>{controller.abort();reject(Object.assign(new Error('quote_service_timeout'),{code:'quote_service_timeout',status:503}));},config.timeoutMs);});
      const result=await Promise.race([work,timeout]);send(200,result);
    }catch(error){
      const code=typeof error?.code==='string'&&/^(quote_|fx_|invalid_fx_|invalid_usdt_)/.test(error.code)?error.code:'quote_service_unavailable';
      send(Number.isInteger(error.status)&&error.status>=400&&error.status<=503?error.status:503,{error:code});
    }finally{clearTimeout(timer);controller.abort();res.off('close',closed);if(admitted)active--;}
  };
}
