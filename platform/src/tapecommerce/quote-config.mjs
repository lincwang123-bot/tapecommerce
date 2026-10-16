// SPDX-License-Identifier: GPL-3.0-only
import { constants } from 'node:fs';
import { open } from 'node:fs/promises';
import path from 'node:path';
import { Wallet, getAddress } from 'ethers';
import { fail } from './errors.mjs';

const address = value => { try { const a=getAddress(value); if (/^0x0{40}$/i.test(a)) throw Error(); return a; } catch { fail('quote_configuration_invalid',503); } };
const decimal = (value,zero=false) => typeof value==='string' && (zero?/^(0|[1-9][0-9]{0,76})$/:/^[1-9][0-9]{0,76}$/).test(value) && BigInt(value)<2n**256n;
export function validateQuoteConfig(input) {
  if(input?.enabled!==true) return Object.freeze({enabled:false,host:'127.0.0.1',port:18373,timeoutMs:15000,maxConcurrent:2});
  const c={...input};
  if(typeof c.tenant!=='string'||!/^[A-Za-z0-9_-]{1,128}$/.test(c.tenant)||!decimal(c.processorNumber,true)||!decimal(c.tokenId)) fail('quote_configuration_invalid',503);
  c.merchant=address(c.merchant);c.checkout=address(c.checkout);c.quoteSigner=address(c.quoteSigner);
  if(c.merchant===c.checkout||!/^0x[0-9a-f]{64}$/i.test(c.checkoutCodeHash)||/^0x0{64}$/i.test(c.checkoutCodeHash)) fail('quote_configuration_invalid',503);
  if(typeof c.authSecret!=='string'||Buffer.byteLength(c.authSecret)<32||Buffer.byteLength(c.authSecret)>256||/\s/.test(c.authSecret)) fail('quote_configuration_invalid',503);
  if(!c.signer||address(c.signer.address)!==c.quoteSigner||typeof c.signer.signTypedData!=='function') fail('quote_signer_mismatch',503);
  if(typeof c.bindingDb!=='string'||!path.isAbsolute(c.bindingDb)) fail('quote_configuration_invalid',503);
  let rpc;try{rpc=new URL(c.rpcUrl);}catch{fail('quote_configuration_invalid',503);}
  if(rpc.protocol!=='https:'||rpc.username||rpc.password||rpc.hash) fail('quote_configuration_invalid',503);
  c.rpcUrl=rpc.href;c.host=c.host??'127.0.0.1';c.port=c.port??18373;c.timeoutMs=c.timeoutMs??15000;c.maxConcurrent=c.maxConcurrent??2;
  if(!['127.0.0.1','::1'].includes(c.host)||!Number.isInteger(c.port)||c.port<1||c.port>65535||!Number.isInteger(c.timeoutMs)||c.timeoutMs<20||c.timeoutMs>30000||!Number.isInteger(c.maxConcurrent)||c.maxConcurrent<1||c.maxConcurrent>8) fail('quote_configuration_invalid',503);
  return Object.freeze(c);
}
async function secretFile(filename) {
  if(typeof filename!=='string'||!path.isAbsolute(filename)) fail('quote_secret_file_invalid',503);
  let handle;
  try {
    handle=await open(filename,constants.O_RDONLY|constants.O_NOFOLLOW);
    const stat=await handle.stat();
    if(!stat.isFile()||stat.size<1||stat.size>1024||(stat.mode&0o077)!==0) fail('quote_secret_file_invalid',503);
    return (await handle.readFile('utf8')).trim();
  } catch { fail('quote_secret_file_invalid',503); } finally { await handle?.close(); }
}
// Environment carries public pins and file paths, never a raw private key.
// Missing/disabled configuration never opens a secret file.
export async function loadQuoteConfig(env=process.env) {
  if(env.TAPE_QUOTE_ENABLED!=='true') return validateQuoteConfig({enabled:false});
  const authSecret=await secretFile(env.TAPE_QUOTE_AUTH_SECRET_FILE);
  const privateKey=await secretFile(env.TAPE_QUOTE_SIGNER_KEY_FILE);
  if(!/^0x[0-9a-f]{64}$/i.test(privateKey)) fail('quote_secret_file_invalid',503);
  let signer;try{signer=new Wallet(privateKey);}catch{fail('quote_secret_file_invalid',503);}
  return validateQuoteConfig({enabled:true,tenant:env.TAPE_QUOTE_TENANT,merchant:env.TAPE_QUOTE_MERCHANT,
    processorNumber:env.TAPE_QUOTE_PROCESSOR_NUMBER,tokenId:env.TAPE_QUOTE_TOKEN_ID,checkout:env.TAPE_QUOTE_CHECKOUT,
    checkoutCodeHash:env.TAPE_QUOTE_CHECKOUT_CODE_HASH,quoteSigner:env.TAPE_QUOTE_SIGNER_ADDRESS,
    rpcUrl:env.TAPE_QUOTE_RPC_URL,bindingDb:env.TAPE_QUOTE_BINDING_DB,authSecret,signer,
    host:env.TAPE_QUOTE_HOST??'127.0.0.1',port:Number(env.TAPE_QUOTE_PORT??18373),timeoutMs:Number(env.TAPE_QUOTE_TIMEOUT_MS??15000)});
}
// The existing Studio challenge/verify transaction is the only author of this
// binding. Reading a signed-in user's DB row does not create a new authorization.
export async function readStudioBinding(config) {
  const {DatabaseSync}=await import('node:sqlite');
  let db;
  try {
    db=new DatabaseSync(config.bindingDb,{readOnly:true});
    return db.prepare('SELECT wallet,verified_at,revision,revoked_at FROM tape_wallet_bindings WHERE shop_id=?').get(config.tenant)??null;
  } finally {db?.close();}
}
