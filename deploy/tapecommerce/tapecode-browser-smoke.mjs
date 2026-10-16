// SPDX-License-Identifier: GPL-3.0-only
// Local-only browser integration: fixed production API requests are intercepted
// and forwarded to the real disposable Go/Anvil demo, with deterministic FX.
import {createRequire} from 'node:module';
import {createServer} from 'node:http';
import {readFile, writeFile, mkdir} from 'node:fs/promises';
import path from 'node:path';
import {execFileSync} from 'node:child_process';
import assert from 'node:assert/strict';
import {startSandbox} from '../../showcase/sandbox.mjs';
import {createShowcase} from '../../showcase/server.mjs';
const require = createRequire(import.meta.url);
const options = {}, args = process.argv.slice(2);
for(let i=0;i<args.length;i+=2){if(!['--bundle','--report-dir','--playwright-module'].includes(args[i])||!args[i+1])throw Error('Usage: --bundle <site-directory> --report-dir <new-evidence-directory> --playwright-module <existing-module-path>');options[args[i]]=args[i+1];}
if(!options['--bundle']||!options['--report-dir']||!options['--playwright-module'])throw Error('All three arguments are required');
const {chromium}=require(path.resolve(options['--playwright-module']));
const bundle=path.resolve(options['--bundle']),reportDir=path.resolve(options['--report-dir']);
await mkdir(reportDir,{recursive:false});
const report={scope:'standard static root and gateway subpath; not TapeCode preview',api:'real local showcase -> Go -> isolated Anvil, deterministic FX fixture and identity fixture',pages:[],flows:[],errors:[],processes:[],startedAt:new Date().toISOString()};
const frontOrigin='https://static.tapecommerce.test',apiOrigin='https://tape.coco9.cc';
let sandbox,app,staticServer,browser,owner,failure;
const listen=server=>new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',()=>resolve(`http://127.0.0.1:${server.address().port}`));});
const alive=pid=>{try{process.kill(pid,0);return true;}catch(error){if(error.code==='ESRCH')return false;throw error;}};
function descendants(pid){const rows=execFileSync('ps',['-axo','pid=,ppid='],{encoding:'utf8'}).trim().split('\n').map(line=>line.trim().split(/\s+/).map(Number));const found=new Set([pid]);let changed=true;while(changed){changed=false;for(const [child,parent]of rows)if(found.has(parent)&&!found.has(child)){found.add(child);changed=true;}}return [...found];}
try{
  sandbox=await startSandbox({onProcess:pid=>report.processes.push({kind:'sandbox',pid}),onFailure:()=>report.errors.push('owned_sandbox_process_failed')});
  app=createShowcase({sandbox,publicOrigin:apiOrigin,corsOrigins:[frontOrigin],rates:{latest:async()=>{const now=Date.now();return{source:'okx',instrumentId:'OKB-USDT',priceField:'last',rateUSDTPerOKB:'300',sourceTimestampMs:now,fetchedAtMs:now};}},lookup:async({processorNumber,tokenId})=>({name:`${tokenId}.2.${processorNumber}.tape`,chainId:196,processorNumber,tokenId,processor:'0x'+'1'.repeat(40),container:'0x'+'2'.repeat(40),holder:'0x'+'3'.repeat(40),opened:true,live:false,blockNumber:1,blockHash:'0x'+'4'.repeat(64),checkedAt:Date.now()})});
  const apiBase=await listen(app.server);
  staticServer=createServer(async(req,res)=>{try{const url=new URL(req.url,'http://local'),relative=decodeURIComponent(url.pathname).replace(/^\/gateway\/site\//,'/').replace(/^\//,'')||'index.html';if(relative.includes('..'))throw Error('path');const filename=path.join(bundle,relative),bytes=await readFile(filename);const type={'.html':'text/html','.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml','.webp':'image/webp'}[path.extname(filename)]||'text/plain';res.writeHead(200,{'content-type':type+'; charset=utf-8'}).end(bytes);}catch{res.writeHead(404).end('not found');}});
  const staticBase=await listen(staticServer);
  owner=await chromium.launchServer({channel:'chrome',headless:true});report.processes.push({kind:'chrome',pid:owner.process().pid});browser=await chromium.connect(owner.wsEndpoint());
  console.log(JSON.stringify({event:'owned_processes',processes:report.processes}));
  for(const mount of ['/','/gateway/site/'])for(const viewport of [{width:1440,height:1100},{width:390,height:844}]){
    const context=await browser.newContext({viewport,acceptDownloads:true});const page=await context.newPage();
    let quoteCount=0,payCount=0,deliveryCount=0;
    page.on('pageerror',error=>report.errors.push(error.message));page.on('console',message=>{if(message.type()==='error')report.errors.push(message.text());});
    await context.route(frontOrigin+'/**',async route=>{const url=new URL(route.request().url());const result=await fetch(staticBase+url.pathname);await route.fulfill({status:result.status,headers:Object.fromEntries(result.headers),body:Buffer.from(await result.arrayBuffer())});});
    await context.route(apiOrigin+'/**',async route=>{
      const request=route.request(),url=new URL(request.url()),incoming=request.headers();assert.equal(incoming.cookie,undefined);assert.equal(incoming.authorization,undefined);
      if(url.pathname.endsWith('/quote'))quoteCount++;if(url.pathname.endsWith('/pay'))payCount++;if(url.pathname.endsWith('/delivery'))deliveryCount++;
      const headers={origin:frontOrigin,'sec-fetch-site':'cross-site','accept':'application/json'};for(const name of ['content-type','x-demo-token','access-control-request-method','access-control-request-headers'])if(incoming[name])headers[name]=incoming[name];
      const result=await fetch(apiBase+url.pathname+url.search,{method:request.method(),headers,...(request.postDataBuffer()?{body:request.postDataBuffer()}:{})});assert.equal(result.headers.get('access-control-allow-origin'),frontOrigin);
      await route.fulfill({status:result.status,headers:Object.fromEntries(result.headers),body:Buffer.from(await result.arrayBuffer())});
    });
    await page.goto(frontOrigin+mount+'index.html#/shop');await page.locator('.catalog-buy').waitFor();
    for(const theme of ['minimal','dark-business','card-catalog','compact-list','editorial','playful']){
      await page.selectOption('#theme-select',theme);await page.locator('.catalog-buy').waitFor();await page.waitForFunction(()=>[...document.images].filter(image=>image.loading!=='lazy').every(image=>image.complete));
      const geometry=await page.evaluate(()=>({width:innerWidth,scroll:document.documentElement.scrollWidth,broken:[...document.images].filter(image=>image.complete&&!image.naturalWidth).map(image=>new URL(image.src).pathname),design:document.body.dataset.design}));assert.ok(geometry.scroll<=geometry.width+1);assert.deepEqual(geometry.broken,[]);report.pages.push({mount,viewport:viewport.width,theme,...geometry});
    }
    await page.selectOption('#theme-select','minimal');const stem=(mount==='/'?'root':'gateway')+'-'+viewport.width;
    await page.screenshot({path:path.join(reportDir,stem+'-shop.png'),fullPage:true});
    await page.locator('.catalog-buy').click();await page.locator('#demo-buy-form button[type=submit]').click();await page.locator('#confirm-demo').click();await page.locator('[data-action=quote]').waitFor();await page.locator('[data-action=quote]').click();await page.locator('[data-action=pay]').waitFor({timeout:25000});
    assert.match(await page.locator('.payment-notice').innerText(),/真实|real|测试|test|模拟|simulat/i);assert.equal(await page.locator('[data-action=download]').count(),0);
    await page.locator('[data-action=pay]').click();await page.locator('[data-action=download]').waitFor({timeout:25000});const downloadReady=page.waitForEvent('download');await page.locator('[data-action=download]').click();const download=await downloadReady;assert.equal(download.suggestedFilename(),'tapecommerce-store-kit.html');const savedDownload=path.join(reportDir,stem+'-download.html');await download.saveAs(savedDownload);const content=await readFile(savedDownload,'utf8');assert.match(content,/Launch checklist/);
    await page.reload();await page.locator('[data-action=download]').waitFor();await page.screenshot({path:path.join(reportDir,stem+'-order.png'),fullPage:true});
    await page.goto(frontOrigin+mount+'index.html#/evidence');await page.locator('[data-action=sample]').click();await page.locator('.record-heading h4').waitFor();assert.match(await page.locator('.record-heading').innerText(),/1\.2\.2\.tape/);
    await page.goto(frontOrigin+mount+'index.html#/merchant');await page.locator('.feature-list').waitFor();await page.locator('[data-action=language]').click();assert.equal(await page.locator('html').getAttribute('lang'),'en');
    report.flows.push({mount,viewport:viewport.width,quoteCount,payCount,deliveryCount,download:download.suggestedFilename(),reloadRestored:true,identityFixture:true,liveWalletVisible:false});assert.equal(quoteCount,1);assert.equal(payCount,1);assert.equal(deliveryCount,1);await context.close();
  }
  assert.deepEqual(report.errors,[]);report.passed=true;
}catch(error){failure=error;report.failure=error.stack;}
finally{
  if(owner){for(const pid of descendants(owner.process().pid))if(!report.processes.some(row=>row.pid===pid))report.processes.push({kind:'chrome_child',pid});}
  if(browser)await browser.close();if(owner)await owner.close();
  if(staticServer){staticServer.closeAllConnections();await new Promise(resolve=>staticServer.close(resolve));report.staticServerClosed=!staticServer.listening;}
  if(app){await app.close();report.apiServerClosed=!app.server.listening;}else await sandbox?.close();
  for(let i=0;i<30&&report.processes.some(row=>alive(row.pid));i++)await new Promise(resolve=>setTimeout(resolve,100));
  report.cleanup=report.processes.map(row=>({...row,exited:!alive(row.pid)}));report.completedAt=new Date().toISOString();
  if(report.cleanup.some(row=>!row.exited)){report.passed=false;failure ||= Error('owned_process_cleanup_failed');}
  await writeFile(path.join(reportDir,'report.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({passed:report.passed,pages:report.pages.length,flows:report.flows.length,errors:report.errors,cleanup:report.cleanup,report:path.join(reportDir,'report.json')},null,2));
}
if(failure)throw failure;
