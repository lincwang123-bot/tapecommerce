// SPDX-License-Identifier: GPL-3.0-only
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {readFile, writeFile, mkdir, lstat, chmod, utimes, rm} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {storefrontAssets, storefrontNotices} from '../../showcase/static-assets.mjs';

export const PUBLIC_API_BASE = 'https://tape.coco9.cc';
export const sourceRoot = fileURLToPath(new URL('../../', import.meta.url));
const PUBLIC_FILES = ['index.html','app.js','ui.js','i18n.js','styles.css','mark.svg'];
export const staticSources = Object.freeze([
  ...PUBLIC_FILES.map(name => [name, `showcase/public/${name}`]),
  ...[...storefrontAssets, ...storefrontNotices].map(name => [name === 'web/styles.css' ? 'storefront-base.css' : name.slice(4), name]),
  ['LICENSE', 'backend/LICENSE'],
].map(Object.freeze));
const forbiddenPath = /(?:^|\/)(?:\.env(?:\..*)?|node_modules|\.git|\.local|data|db|logs|runtime|backend|platform|server\.mjs|sandbox\.mjs|.*\.(?:sqlite|db|pem|key))(?=\/|$)/i;
const hash = bytes => createHash('sha256').update(bytes).digest('hex');

function mountPath(value = './') {
  if (value === './') return value;
  if (typeof value !== 'string' || !/^\/(?:[A-Za-z0-9_-]+\/)*$/.test(value)) throw Error('mountPath must be ./, /, or a known absolute directory such as /gateway/site/');
  return value;
}
function replaceOnce(source, before, after, label) {
  if (source.split(before).length !== 2) throw Error(`Source contract changed: ${label}`);
  return source.replace(before, after);
}
async function safeRead(root, relative) {
  let current = root;
  for (const part of relative.split('/')) {
    if (!part || part === '.' || part === '..') throw Error('Invalid source path');
    current = path.join(current, part);
    if ((await lstat(current)).isSymbolicLink()) throw Error(`Symlink source refused: ${relative}`);
  }
  const info = await lstat(current);
  if (!info.isFile() || info.size > 8 * 1024 * 1024) throw Error(`Invalid static source: ${relative}`);
  return readFile(current);
}
const licensing = `# TapeCommerce static frontend licenses

The independently authored TapeCommerce browser UI and commerce integration are licensed under GPL-3.0-only. The complete GNU GPL version 3 text is included in LICENSE. This archive contains their readable JavaScript, CSS and HTML source; it contains no backend executable, database, private configuration or wallet secret.

The existing six Coco9 Storefront UI themes, rendering utilities, styles and 12 WebP images retain their MIT license and original bytes. See storefront-ui/LICENSE, storefront-ui/ASSETS.md and storefront-ui/UPSTREAM.md. The added storefront-ui/commerce.js follows its GPL-3.0-only SPDX declaration.

Source and build instructions: https://github.com/lincwang123-bot/tapecommerce
The hosted API is a separate service at https://tape.coco9.cc. This static archive does not independently run the API or chain. Demo checkout uses an isolated EVM and test balance, with no real funds. Real mainnet payment is not enabled by publishing this archive.
`;

/** Copies only reviewed public bytes; transforms copies, never the working tree. */
export async function collectStaticFiles({root = sourceRoot, resourceMount = './'} = {}) {
  resourceMount = mountPath(resourceMount);
  const files = new Map();
  for (const [target, source] of staticSources) {
    if (files.has(target) || forbiddenPath.test(target) || target.startsWith('/') || target.includes('..')) throw Error(`Invalid static destination: ${target}`);
    files.set(target, await safeRead(root, source));
  }
  let html = files.get('index.html').toString('utf8');
  html = replaceOnce(html, 'href="/storefront-base.css"', `href="${resourceMount}storefront-base.css"`, 'base CSS URL');
  html = replaceOnce(html, 'href="/storefront-ui/styles.css"', `href="${resourceMount}storefront-ui/styles.css"`, 'theme CSS URL');
  for (const name of ['styles.css','mark.svg']) html = replaceOnce(html, `href="./${name}"`, `href="${resourceMount}${name}"`, name);
  html = replaceOnce(html, 'src="./app.js"', `src="${resourceMount}app.js"`, 'entry script');
  const csp = `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self' ${PUBLIC_API_BASE}; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'`;
  html = replaceOnce(html, '<meta charset="utf-8">', `<meta charset="utf-8"><meta name="referrer" content="no-referrer"><script src="${resourceMount}runtime-policy.js"></script>`, 'static policy');
  files.set('runtime-policy.js', Buffer.from(`// SPDX-License-Identifier: GPL-3.0-only\n// An opaque host viewer supplies its own CSP and rewrites scripts to Blob URLs.\nif (globalThis.origin !== 'null') {\n  const policy = document.createElement('meta');\n  policy.httpEquiv = 'Content-Security-Policy';\n  policy.content = ${JSON.stringify(csp)};\n  document.head.append(policy);\n}\n`));
  files.set('index.html', Buffer.from(html));

  let app = files.get('app.js').toString('utf8');
  const imports = app.match(/from '\/storefront-ui\//g) || [];
  if (imports.length !== 4) throw Error('Source contract changed: storefront entry imports');
  app = app.replaceAll("from '/storefront-ui/", "from './storefront-ui/");
  app = replaceOnce(app, 'const api = createApi();', `const api = createApi({apiBase: '${PUBLIC_API_BASE}'});`, 'fixed API origin');
  app = replaceOnce(app, 'renderStorefront({template:theme,', `renderStaticStorefront({assetBase:'${resourceMount}storefront-ui/assets',template:theme,`, 'gateway image base');
  app = replaceOnce(app, 'const root = document.body;', `function renderStaticStorefront(model) {
  const html = renderStorefront(model);
  // The official viewer rewrites dynamic src, but not dynamic srcset.
  return globalThis.origin === 'null' ? html.replace(/\\s+srcset="[^"]*"/g, '') : html;
}
const root = document.body;`, 'opaque responsive image adapter');
  files.set('app.js', Buffer.from(app));
  const client = files.get('ui.js').toString('utf8');
  if (!/createApi\(\{[^]*?apiBase/.test(client) || !client.includes(PUBLIC_API_BASE) || !/credentials:\s*['"]omit['"]/.test(client)) throw Error('Static API client contract is not ready: require allowlisted apiBase and credentials omit');
  files.set('LICENSING.md', Buffer.from(licensing));
  for (const [name, bytes] of files) {
    if (path.extname(name) === '.webp') continue;
    const text = bytes.toString('utf8');
    if (/\/Users\/|-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----|\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})\b|\bAKIA[A-Z0-9]{16}\b/.test(text)) throw Error(`Private-looking material in allowlisted file: ${name}`);
    if (/https?:\/\/[^\s/:'"]+:[^\s/@'"]+@/.test(text)) throw Error(`Credential URL in allowlisted file: ${name}`);
  }
  validateResourceClosure(files, {resourceMount});
  return files;
}

function references(name, bytes) {
  const text = bytes.toString('utf8'), refs = [];
  if (name.endsWith('.html')) {
    for (const match of text.matchAll(/<(?:script|link|img)\b[^>]*?\b(?:src|href)=["']([^"']+)["']/g)) refs.push(match[1]);
  } else if (name.endsWith('.js')) {
    for (const match of text.matchAll(/(?:\bimport|\bexport)[^;]*?\bfrom\s*["']([^"']+)["']/g)) refs.push(match[1]);
    for (const match of text.matchAll(/\bimport\s*["']([^"']+)["']/g)) refs.push(match[1]);
  } else if (name.endsWith('.css')) {
    for (const match of text.matchAll(/(?:url\(\s*|@import\s*)["']?([^"')\s;]+)["']?\s*\)?/g)) refs.push(match[1]);
  }
  return refs;
}
/** Checks every static ESM/HTML/CSS reference at the domain root and gateway subpath. */
export function validateResourceClosure(files, {resourceMount = './'} = {}) {
  resourceMount = mountPath(resourceMount);
  const mounts = resourceMount === './' ? ['/', '/gateway/example/'] : [resourceMount];
  for (const mount of mounts) {
    for (const [name, bytes] of files) {
      if (forbiddenPath.test(name) || name.startsWith('/') || name.split('/').includes('..')) throw Error(`Forbidden static path: ${name}`);
      for (const ref of references(name, bytes)) {
        if (ref.startsWith('#') || ref.startsWith('data:')) continue;
        const resolved = new URL(ref, `https://static.example${mount}${name}`);
        if (resolved.origin !== 'https://static.example' || !resolved.pathname.startsWith(mount)) throw Error(`Resource escapes mount: ${name} -> ${ref}`);
        const target = decodeURIComponent(resolved.pathname.slice(mount.length));
        if (!files.has(target)) throw Error(`Missing static dependency: ${name} -> ${target}`);
      }
    }
    for (const design of ['minimal','orbital','warm','arcade','editorial','neon']) {
      for (const width of [768,1536]) if (!files.has(`storefront-ui/assets/${design}-${width}.webp`)) throw Error(`Missing theme image: ${design}-${width}`);
    }
  }
  return {mounts,files:files.size};
}

export async function buildStaticBundle({out, root = sourceRoot, resourceMount = './'} = {}) {
  if (!out) throw Error('A new --out directory is required');
  resourceMount = mountPath(resourceMount);
  out = path.resolve(out); root = path.resolve(root);
  if (out === root || root.startsWith(out + path.sep)) throw Error('Output must not replace the source root or an ancestor');
  const files = await collectStaticFiles({root,resourceMount});
  const zipPath = out + '.zip', manifestPath = out + '.SHA256_MANIFEST.json', readmePath = out + '.IMPORT_README.md';
  for (const target of [out,zipPath,manifestPath,readmePath]) {
    try { await lstat(target); throw Error('Output exists; refusing overwrite'); } catch (error) { if (error.code !== 'ENOENT') throw error; }
  }
  let created = false; const sidecars = [];
  try {
    await mkdir(out, {mode:0o755}); created = true;
    const site = out;
    const sorted = [...files].sort(([a],[b]) => a.localeCompare(b));
    for (const [name,bytes] of sorted) {
      const destination = path.join(site,name);
      await mkdir(path.dirname(destination),{recursive:true,mode:0o755});
      await writeFile(destination,bytes,{flag:'wx',mode:0o644}); await chmod(destination,0o644);
      await utimes(destination,new Date('2000-01-01T00:00:00Z'),new Date('2000-01-01T00:00:00Z'));
    }
    const zipName = path.basename(zipPath), temporaryZip = path.join(out,'.upload-archive.zip');
    execFileSync('zip',['-q','-9','-X',temporaryZip,...sorted.map(([name])=>name)],{cwd:site,env:{...process.env,TZ:'UTC'},maxBuffer:1024*1024,timeout:30000});
    const archive = await readFile(temporaryZip); await rm(temporaryZip);
    await writeFile(zipPath,archive,{flag:'wx',mode:0o644}); sidecars.push(zipPath);
    const manifest = {schemaVersion:1,kind:'tapecommerce_static_tapecode_candidate',createdAt:new Date().toISOString(),
      published:false,apiBase:PUBLIC_API_BASE,resourceMount,entry:'index.html',
      prerequisites:['Static host must serve JS/CSS/WebP with correct MIME types.','Default relative mode requires a directory URL ending in / or an index.html URL.','API must permit the exact final gateway Origin; this build does not change CORS.','A restrictive gateway CSP must allow connections to https://tape.coco9.cc.','TapeCode opaque previews require the separate anonymous-demo-only opaque-origin opt-in; it is off by default and does not enable real payment.','Opaque previews keep credentials only in document memory and cannot directly download; delivery content and license are shown for copying.','Final TapeCode import and public gateway still need target-specific verification.'],
      excluded:['backend runtime','environment files','database and order state','merchant private configuration','wallet secrets','production wallet module'],
      transformedFiles:['index.html','app.js'],generatedFiles:['runtime-policy.js','LICENSING.md'],archive:{path:zipName,bytes:archive.length,sha256:hash(archive)},
      files:sorted.map(([name,bytes])=>({path:name,bytes:bytes.length,sha256:hash(bytes)}))};
    await writeFile(manifestPath,JSON.stringify(manifest,null,2)+'\n',{flag:'wx',mode:0o644}); sidecars.push(manifestPath);
    await writeFile(readmePath,`# TapeCommerce static import candidate\n\nSelect the ${path.basename(out)}/ folder or import ${zipName}; index.html is at the folder and archive root. ${path.basename(manifestPath)} is outside the archive to avoid a recursive digest.\n\nThis is the complete existing browser UI and original six theme assets. It requires the hosted API at ${PUBLIC_API_BASE}; it does not bundle or run that backend. Sandbox labels and real server order/quote/payment/delivery requests are retained. No real mainnet payment is enabled by this upload.\n\nResource mount: ${resourceMount}. In relative mode use a directory URL ending in / or an index.html URL. Do not change or overwrite an unrelated Tape container. The final X Layer container and gateway origin have not been selected by this builder. Configure the API's exact Origin allowlist before testing the final imported site. The gateway must serve .js as JavaScript, .css as CSS and .webp as images, and permit API connections in its CSP. The included meta CSP cannot override stricter host response headers.\n\nTapeCode editor preview uses an iframe without allow-same-origin, producing Origin: null. Only the separate anonymous demo API opt-in may support this mode; it remains disabled by default and never enables real payment. The output adapts relative images, explicit hash routing and memory-only credentials. The host supplies CSP for its opaque Blob runtime; ordinary static hosting receives the strict policy from runtime-policy.js. Direct downloads are not attempted in opaque previews: actual authorized delivery content and license are displayed for selection/copy. Reloading an opaque preview loses its memory-only credential; start a new demo in the ordinary browser site for downloadable delivery, without putting the credential into any external URL. Final TapeCode import and public gateway require target-specific verification.\n\nNo upload, service restart, DNS change, payment, or on-chain transaction is performed by this builder.\n`,{flag:'wx',mode:0o644}); sidecars.push(readmePath);
    return {directory:out,site,zip:zipPath,manifest:manifestPath,files:files.size,archiveSha256:manifest.archive.sha256,published:false};
  } catch (error) { if (created) await rm(out,{recursive:true,force:true}); for (const target of sidecars) await rm(target,{force:true}); throw error; }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const options = {}, args = process.argv.slice(2);
  for (let i=0;i<args.length;i+=2) {
    if (!['--out','--mount-path'].includes(args[i]) || !args[i+1] || options[args[i]]) throw Error('Usage: node deploy/tapecommerce/tapecode-package.mjs --out <new-directory> [--mount-path /known/gateway/path/]');
    options[args[i]]=args[i+1];
  }
  console.log(JSON.stringify(await buildStaticBundle({out:options['--out'],resourceMount:options['--mount-path'] || './'}),null,2));
}
