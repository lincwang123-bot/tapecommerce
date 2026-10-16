// SPDX-License-Identifier: GPL-3.0-only
// Supply a separately downloaded official frame.html for local compatibility QA.
// The reference file is never copied into a public site or changed by this test.
import {createRequire} from 'node:module';
import {createServer} from 'node:http';
import {readFile, writeFile, mkdir} from 'node:fs/promises';
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import path from 'node:path';
import assert from 'node:assert/strict';
import {startSandbox} from '../../showcase/sandbox.mjs';
import {createShowcase} from '../../showcase/server.mjs';
const options={},args=process.argv.slice(2);
for(let i=0;i<args.length;i+=2){if(!['--bundle','--report-dir','--playwright-module','--frame-reference'].includes(args[i])||!args[i+1])throw Error('Required: --bundle --report-dir --playwright-module --frame-reference');options[args[i]]=args[i+1];}
if(Object.keys(options).length!==4)throw Error('All four arguments are required');
const require=createRequire(import.meta.url),{chromium}=require(path.resolve(options['--playwright-module']));
const bundle=path.resolve(options['--bundle']),reportDir=path.resolve(options['--report-dir']),reference=await readFile(path.resolve(options['--frame-reference']),'utf8');
await mkdir(reportDir,{recursive:false});
const manifest=JSON.parse(await readFile(bundle+'.SHA256_MANIFEST.json'));
const mime=name=>({'.html':'text/html','.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml','.webp':'image/webp'}[path.extname(name)]||'text/plain');
const files=await Promise.all(manifest.files.map(async row=>({path:row.path,type:mime(row.path),base64:(await readFile(path.join(bundle,row.path))).toString('base64')})));
const report={scope:'official TapeCode frame source in a local sandbox=allow-scripts iframe; not hosted TapeCode UI',frameSHA256:createHash('sha256').update(reference).digest('hex'),api:'real local Node -> Go -> isolated Anvil, deterministic FX and identity fixtures; opaque demo opt-in enabled only in local harness',pages:[],flows:[],errors:[],hostDiagnostics:[],processes:[],startedAt:new Date().toISOString()};
let sandbox,app,server,owner,browser,failure;
const alive=pid=>{try{process.kill(pid,0);return true;}catch(error){if(error.code==='ESRCH')return false;throw error;}};
const listen=server=>new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',()=>resolve(`http://127.0.0.1:${server.address().port}`));});
function descendants(pid){const rows=execFileSync('ps',['-axo','pid=,ppid='],{encoding:'utf8'}).trim().split('\n').map(line=>line.trim().split(/\s+/).map(Number));const found=new Set([pid]);let changed=true;while(changed){changed=false;for(const [child,parent]of rows)if(found.has(parent)&&!found.has(child)){found.add(child);changed=true;}}return [...found];}
try{
 sandbox=await startSandbox({onProcess:pid=>report.processes.push({kind:'sandbox',pid}),onFailure:()=>report.errors.push('owned_sandbox_failed')});
 app=createShowcase({sandbox,publicOrigin:'https://tape.coco9.cc',allowOpaqueDemoOrigin:true,rates:{latest:async()=>{const now=Date.now();return{source:'okx',instrumentId:'OKB-USDT',priceField:'last',rateUSDTPerOKB:'300',sourceTimestampMs:now,fetchedAtMs:now};}},lookup:async({processorNumber,tokenId})=>({name:`${tokenId}.2.${processorNumber}.tape`,chainId:196,processorNumber,tokenId,processor:'0x'+'1'.repeat(40),container:'0x'+'2'.repeat(40),holder:'0x'+'3'.repeat(40),opened:true,live:false,blockNumber:1,blockHash:'0x'+'4'.repeat(64),checkedAt:Date.now()})});
 const apiBase=await listen(app.server);
 server=createServer((req,res)=>{res.setHeader('content-type','text/html; charset=utf-8');res.end(req.url==='/frame.html'?reference:'<!doctype html><html><head><meta charset="utf-8"><title>Local TapeCode frame compatibility harness</title></head><body style="margin:0"></body></html>');});const origin=await listen(server);
 owner=await chromium.launchServer({channel:'chrome',headless:true});report.processes.push({kind:'chrome',pid:owner.process().pid});browser=await chromium.connect(owner.wsEndpoint());console.log(JSON.stringify({event:'owned_processes',processes:report.processes}));
 for(const viewport of [{width:1440,height:1100},{width:390,height:844}]){
  const context=await browser.newContext({viewport,acceptDownloads:true}),page=await context.newPage();let downloads=0,quoteCount=0,payCount=0,deliveryCount=0;
  page.on('download',()=>downloads++);page.on('pageerror',error=>report.errors.push(error.message));page.on('console',message=>{if(message.type()==='error'){if(message.text().includes('site.tape.invalid'))report.hostDiagnostics.push(message.text());else report.errors.push(message.text());}});
  await context.route('https://tape.coco9.cc/**',async route=>{const request=route.request(),url=new URL(request.url()),incoming=request.headers();assert.equal(incoming.cookie,undefined);assert.equal(incoming.authorization,undefined);assert.equal(incoming.origin,'null');if(url.pathname.endsWith('/quote'))quoteCount++;if(url.pathname.endsWith('/pay'))payCount++;if(url.pathname.endsWith('/delivery'))deliveryCount++;
    const headers={origin:'null','sec-fetch-site':'cross-site',accept:'application/json','x-real-ip':`127.0.0.${viewport.width===390?2:1}`};for(const key of ['content-type','x-demo-token','access-control-request-method','access-control-request-headers'])if(incoming[key])headers[key]=incoming[key];
    const response=await fetch(apiBase+url.pathname+url.search,{method:request.method(),headers,...(request.postDataBuffer()?{body:request.postDataBuffer()}:{})});assert.equal(response.headers.get('access-control-allow-origin'),'null');await route.fulfill({status:response.status,headers:Object.fromEntries(response.headers),body:Buffer.from(await response.arrayBuffer())});
  });
  await page.goto(origin+'/');await page.evaluate(({files})=>{
    window.hostMessages=[];const frame=document.createElement('iframe');frame.setAttribute('sandbox','allow-scripts');frame.style.cssText='border:0;width:100%;height:100vh;display:block';
    addEventListener('message',event=>{if(event.source!==frame.contentWindow)return;const message=event.data;window.hostMessages.push({type:message?.type,message:message?.message,path:message?.path,url:message?.url});if(message?.type==='hp-ready'){const payload=files.map(file=>({path:file.path,type:file.type,bytes:Uint8Array.from(atob(file.base64),char=>char.charCodeAt(0)).buffer}));frame.contentWindow.postMessage({type:'hp-render',entry:'index.html',known:files.map(file=>file.path),files:payload},'*',payload.map(file=>file.bytes));}else if(message?.type==='hp-need'){const file=files.find(file=>file.path===message.path);frame.contentWindow.postMessage({type:'hp-file',id:message.id,path:message.path,ok:Boolean(file),...(file?{bytes:Uint8Array.from(atob(file.base64),char=>char.charCodeAt(0)).buffer,contentType:file.type}:{})},'*');}});
    frame.src='/frame.html';document.body.append(frame);
  },{files});
  const frame=page.frameLocator('iframe');
  const navigateHeader=async href=>{const toggle=frame.locator('.nav-toggle');if(await toggle.isVisible()&&await toggle.getAttribute('aria-expanded')!=='true')await toggle.click();await frame.locator(`header a[href${href.startsWith('#/order/')?'^=':'='}\"${href}\"]`).click();};await frame.locator('.catalog-buy').waitFor({timeout:20000});
  for(const theme of ['minimal','dark-business','card-catalog','compact-list','editorial','playful']){
    await frame.locator('#theme-select').selectOption(theme);await frame.locator('.catalog-buy').waitFor();await frame.locator('.scene-image').evaluate(image=>image.decode());
    const geometry=await frame.locator('body').evaluate(()=>({origin:self.origin,width:innerWidth,scroll:document.documentElement.scrollWidth,broken:[...document.images].filter(image=>!image.naturalWidth).map(image=>image.getAttribute('src')),responsiveCandidates:document.querySelectorAll('.scene-image[srcset]').length,design:document.body.dataset.design}));assert.equal(geometry.origin,'null');assert.ok(geometry.scroll<=geometry.width+1);assert.deepEqual(geometry.broken,[]);assert.equal(geometry.responsiveCandidates,0);report.pages.push({viewport:viewport.width,theme,...geometry});
  }
  await frame.locator('#theme-select').selectOption('minimal');await page.screenshot({path:path.join(reportDir,`frame-${viewport.width}-shop.png`),fullPage:true});
  await frame.locator('.catalog-buy').click();await frame.locator('#demo-buy-form').waitFor();await frame.locator('#demo-buy-form [data-action=preview-submit]').click();await frame.locator('#confirm-demo').click();await frame.locator('[data-action=quote]').waitFor();await frame.locator('[data-action=quote]').click();await frame.locator('[data-action=pay]').waitFor({timeout:20000});assert.equal(await frame.locator('.delivery-content').count(),0);
  await frame.locator('[data-action=pay]').click();await frame.locator('[data-action=download]').waitFor({timeout:25000});await frame.locator('[data-action=download]').click();await frame.locator('.delivery-content textarea').waitFor();assert.match(await frame.locator('.delivery-content textarea').inputValue(),/Launch checklist/);assert.ok((await frame.locator('.license code').innerText()).length>10);assert.equal(await frame.locator('.download-feedback').count(),0);assert.equal(downloads,0);assert.match(await frame.locator('.order-panel').innerText(),/隔离预览禁止直接下载/);
  await page.screenshot({path:path.join(reportDir,`frame-${viewport.width}-delivery.png`),fullPage:true});
  await frame.locator('a[href="#/shop"]').first().click();await frame.locator('.catalog-buy').waitFor();await navigateHeader('#/order/');await frame.locator('.delivery-content textarea').waitFor();
  await navigateHeader('#/evidence');await frame.locator('[data-action=sample]').click();await frame.locator('.record-heading h4').waitFor();await navigateHeader('#/merchant');await frame.locator('.feature-list').waitFor();await frame.locator('[data-action=language]').click();assert.equal(await frame.locator('html').getAttribute('lang'),'en');
  const messages=await page.evaluate(()=>window.hostMessages);assert.ok(!messages.some(message=>message.type==='hp-error'||message.type==='hp-external'||message.type==='hp-nav'));assert.equal(quoteCount,1);assert.equal(payCount,1);assert.equal(deliveryCount,1);report.flows.push({viewport:viewport.width,quoteCount,payCount,deliveryCount,downloads,contentAndLicenseShown:true,memoryCredentialSurvivesNavigation:true,externalCredentialTransfer:false,identityFixture:true});await context.close();
 }
 assert.deepEqual(report.errors,[]);report.passed=true;
}catch(error){failure=error;report.failure=error.stack;}
finally{
 if(owner)for(const pid of descendants(owner.process().pid))if(!report.processes.some(row=>row.pid===pid))report.processes.push({kind:'chrome_child',pid});if(browser)await browser.close();if(owner)await owner.close();if(server){server.closeAllConnections();await new Promise(resolve=>server.close(resolve));report.frameServerClosed=!server.listening;}if(app){await app.close();report.apiServerClosed=!app.server.listening;}else await sandbox?.close();
 for(let i=0;i<30&&report.processes.some(row=>alive(row.pid));i++)await new Promise(resolve=>setTimeout(resolve,100));report.cleanup=report.processes.map(row=>({...row,exited:!alive(row.pid)}));if(report.cleanup.some(row=>!row.exited)){report.passed=false;failure ||= Error('owned_process_cleanup_failed');}report.completedAt=new Date().toISOString();await writeFile(path.join(reportDir,'report.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({passed:report.passed,pages:report.pages.length,flows:report.flows.length,errors:report.errors,cleanup:report.cleanup,report:path.join(reportDir,'report.json')},null,2));
}
if(failure)throw failure;
