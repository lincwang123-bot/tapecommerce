// SPDX-License-Identifier: GPL-3.0-only
// Only these original presentation files may be served or copied into a demo
// runtime. Never expose web/app.js, API clients, Studio or configuration files.
const themes = ['minimal', 'orbital', 'warm', 'arcade', 'editorial', 'neon'];
export const storefrontAssets = [
  'web/styles.css', 'web/factory-theme.js',
  ...['index.js', 'shared.js', 'registry.js', 'laptop.js', 'navigation.js', 'commerce.js', 'styles.css']
    .map(name => `web/storefront-ui/${name}`),
  ...themes.map(name => `web/storefront-ui/themes/${name}.js`),
  ...themes.flatMap(name => [768, 1536].map(size => `web/storefront-ui/assets/${name}-${size}.webp`)),
];
export const storefrontNotices = ['web/storefront-ui/LICENSE', 'web/storefront-ui/ASSETS.md', 'web/storefront-ui/UPSTREAM.md'];
export const storefrontRoutes = [...storefrontAssets, ...storefrontNotices].map(file => [
  file === 'web/styles.css' ? '/storefront-base.css' : file.slice(3),
  [`../${file}`, file.endsWith('.webp') ? 'image/webp' : file.endsWith('.css') ? 'text/css; charset=utf-8' : file.endsWith('.js') ? 'text/javascript; charset=utf-8' : 'text/plain; charset=utf-8'],
]);
