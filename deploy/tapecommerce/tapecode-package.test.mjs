// SPDX-License-Identifier: GPL-3.0-only
import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp, readFile, mkdir, writeFile, copyFile, rm, symlink, unlink, readdir} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {collectStaticFiles, validateResourceClosure, buildStaticBundle, staticSources, sourceRoot, PUBLIC_API_BASE} from './tapecode-package.mjs';
import {renderStorefront,themes} from '../../web/storefront-ui/index.js';
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
async function temp(t) { const dir=await mkdtemp(path.join(tmpdir(),'tapecommerce-static-'));t.after(()=>rm(dir,{recursive:true,force:true}));return dir; }
async function fixture(t) {const root=await temp(t);for(const [,source] of staticSources){await mkdir(path.dirname(path.join(root,source)),{recursive:true});await copyFile(path.join(sourceRoot,source),path.join(root,source));}return root;}

test('allowlist copies only static public files and preserves original template/asset bytes',async()=>{
  const files=await collectStaticFiles();assert.equal(files.size,39);assert.ok(files.has('index.html'));assert.ok(files.has('LICENSE'));assert.ok(files.has('storefront-ui/LICENSE'));assert.ok(files.has('storefront-ui/ASSETS.md'));assert.ok(files.has('storefront-ui/UPSTREAM.md'));
  for(const [name,source] of staticSources){if(name.startsWith('storefront-ui/'))assert.deepEqual(files.get(name),await readFile(path.join(sourceRoot,source)),name);}
  for(const name of files.keys())assert.doesNotMatch(name,/(?:^|\/)(?:backend|platform|\.env|data|db|node_modules|server\.mjs|sandbox\.mjs)/);
  assert.doesNotMatch([...files.keys()].join('\n'),/checkout\.json|tapecommerce-checkout\.js|config\.json|order/i);
});
test('complete HTML/ESM/CSS closure resolves at root, directory gateway and explicit gateway mount',async()=>{
  const files=await collectStaticFiles();assert.deepEqual(validateResourceClosure(files).mounts,['/','/gateway/example/']);
  for(const prefix of ['/','/gateway/site/']){const mounted=await collectStaticFiles({resourceMount:prefix});assert.deepEqual(validateResourceClosure(mounted,{resourceMount:prefix}).mounts,[prefix]);assert.match(mounted.get('index.html').toString(),new RegExp('href="'+prefix+'storefront-base.css"'));}
  for(const bad of ['https://evil.test/','//evil.test/','/../secret/','/bad','/a?x/','/a%2fb/'])await assert.rejects(collectStaticFiles({resourceMount:bad}),/mountPath/);
});
test('six original template images resolve under both document mount shapes without import.meta',async()=>{
  const files=await collectStaticFiles();
  for(const prefix of ['/','/gateway/site/'])for(const theme of themes){const assetBase='./storefront-ui/assets';const html=renderStorefront({template:theme.id,assetBase});const src=html.match(/<img[^>]+src="([^"]+)"/)[1];const url=new URL(src,`https://example.test${prefix}index.html`);assert.ok(url.pathname.startsWith(prefix));assert.ok(files.has(url.pathname.slice(prefix.length)));assert.match(src,new RegExp(theme.design+'-1536\\.webp$'));}
});
test('missing assets, leaked runtime paths and mount-escaping imports are rejected',async()=>{
  const files=await collectStaticFiles();const missing=new Map(files);missing.delete('storefront-ui/shared.js');assert.throws(()=>validateResourceClosure(missing),/Missing static dependency/);
  const image=new Map(files);image.delete('storefront-ui/assets/warm-768.webp');assert.throws(()=>validateResourceClosure(image),/Missing theme image/);
  const leaked=new Map(files);leaked.set('.env',Buffer.from('not-a-real-secret'));assert.throws(()=>validateResourceClosure(leaked),/Forbidden static path/);
  const escaping=new Map(files);escaping.set('app.js',Buffer.from("import {x} from '/storefront-ui/index.js';"));assert.throws(()=>validateResourceClosure(escaping),/Resource escapes mount/);
});
test('unlisted environment, database and extra public files never enter the package',async t=>{
  const root=await fixture(t);await writeFile(path.join(root,'showcase/public/.env'),'NOT_A_REAL_SECRET=must-not-export');await writeFile(path.join(root,'showcase/public/orders.sqlite'),'synthetic private orders');await writeFile(path.join(root,'showcase/public/extra.js'),'doNotExport()');const files=await collectStaticFiles({root});assert.equal(files.size,39);assert.ok(![...files.values()].some(bytes=>bytes.includes('must-not-export')||bytes.includes('doNotExport')));
});
test('source symlinks are rejected without following them into other files',async t=>{
  const root=await fixture(t),source=path.join(root,'showcase/public/ui.js');await unlink(source);await symlink(path.join(sourceRoot,'showcase/public/ui.js'),source);await assert.rejects(collectStaticFiles({root}),/Symlink source refused/);
});
test('exported API client remains functional with fixed external origin and explicit order token',async()=>{
  const files=await collectStaticFiles(),app=files.get('app.js').toString();assert.match(app,/createApi\(\{apiBase: 'https:\/\/tape\.coco9\.cc'\}\)/);assert.ok(app.includes("assetBase:'./storefront-ui/assets'"));
  const client=await import('data:text/javascript;base64,'+files.get('ui.js').toString('base64'));let captured;const api=client.createApi({apiBase:PUBLIC_API_BASE,fetchImpl:async(url,options)=>{captured={url,options};return new Response(JSON.stringify({order:{id:'test-order',status:'quoted'}}));}});
  const result=await api.request('/api/demo/orders/test-order/quote',{method:'POST',body:{},token:'synthetic-test-token'});assert.equal(result.order.status,'quoted');assert.equal(captured.url,PUBLIC_API_BASE+'/api/demo/orders/test-order/quote');assert.equal(captured.options.credentials,'omit');assert.equal(captured.options.headers['X-Demo-Token'],'synthetic-test-token');assert.equal(captured.options.redirect,'error');api.dispose();
});
test('ZIP entries exactly match the site and external manifest hashes; exports are deterministic and non-overwriting',async t=>{
  const root=await temp(t),out=path.join(root,'TapeCommerce-DeWEB');const result=await buildStaticBundle({out});const manifest=JSON.parse(await readFile(result.manifest));assert.equal(result.site,out);assert.equal(result.zip,out+'.zip');assert.equal(manifest.files.length,39);assert.equal(manifest.published,false);assert.equal(manifest.apiBase,PUBLIC_API_BASE);
  const zipEntries=execFileSync('unzip',['-Z','-1',result.zip],{encoding:'utf8'}).trim().split('\n');assert.deepEqual(zipEntries,manifest.files.map(row=>row.path));execFileSync('unzip',['-tq',result.zip]);
  for(const file of manifest.files){const bytes=await readFile(path.join(out,file.path));assert.equal(bytes.length,file.bytes);assert.equal(digest(bytes),file.sha256);}assert.equal(digest(await readFile(result.zip)),manifest.archive.sha256);assert.ok(!(await readdir(out)).some(name=>name.endsWith('.zip')||name.includes('MANIFEST')));
  const second=await buildStaticBundle({out:path.join(root,'second')});assert.equal(second.archiveSha256,result.archiveSha256);await assert.rejects(buildStaticBundle({out}),/Output exists/);assert.equal(digest(await readFile(result.zip)),result.archiveSha256);
  for(const name of ['app.js','ui.js','storefront-ui/commerce.js'])execFileSync(process.execPath,['--check','--input-type=module'],{input:await readFile(path.join(out,name))});
});
