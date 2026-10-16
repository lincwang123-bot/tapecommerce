// Framework-free presentation only: no fetch, credentials, storage or payment logic.
import {themeFor} from './registry.js';
import {escapeHTML as e, safeImage, safeProductLink} from './shared.js';
import {minimal} from './themes/minimal.js';
import {orbital} from './themes/orbital.js';
import {warm} from './themes/warm.js';
import {arcade} from './themes/arcade.js';
import {editorial} from './themes/editorial.js';
import {neon} from './themes/neon.js';
export {themes, themeFor} from './registry.js';
export {bindLaptop} from './laptop.js';
const renderers = {minimal, orbital, warm, arcade, editorial, neon};
export function renderStorefront(model = {}) {
  const theme = themeFor(model.template);
  return `<div class="factory-store design-${theme.design}">${renderers[theme.design]({...model,categories:model.categories || []})}</div>`;
}
export function renderProduct(product, template, index = 0) {
  const theme = themeFor(template), image = safeImage(product.image), url = safeProductLink(product.href);
  const initials = [...String(product.name || '商品')].slice(0,2).join('');
  return `<article class="product catalog-product item-${index%4}"><a class="catalog-art" href="${e(url)}" tabindex="-1" aria-hidden="true"><span class="catalog-monogram">${e(initials)}</span>${image ? `<img data-product-image src="${e(image)}" alt="" loading="lazy" decoding="async" referrerpolicy="no-referrer">` : ''}</a><div class="catalog-product-copy"><span class="catalog-category">${e(product.category || '数字商品')}</span><h2><a href="${e(url)}">${e(product.name)}</a></h2><p class="catalog-description">${e(product.description)}</p><div class="catalog-purchase"><span class="catalog-price">${e(product.price)}<small> / 份</small></span><a class="catalog-buy" href="${e(url)}" aria-label="查看商品：${e(product.name)}">${theme.design==='arcade'?'查看装备':'查看商品'} <span aria-hidden="true">↗</span></a></div>${product.soldOut ? '<span class="catalog-stock" role="status">暂时售罄</span>' : ''}</div></article>`;
}
