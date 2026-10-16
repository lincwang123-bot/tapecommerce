export const escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
export function safeImage(value) {
  if (typeof value !== 'string' || /[\\\s\u0000-\u001f]/.test(value)) return '';
  if (/^\/(?!\/)/.test(value)) return value;
  try { const url = new URL(value); return url.protocol === 'https:' && !url.username && !url.password ? url.href : ''; } catch { return ''; }
}
export const safeProductLink = value => typeof value === 'string' && /^#\/product\//.test(value) ? value : '#/shop';
export function heroImage(design, {assetBase = '/storefront-ui/assets'} = {}) {
  const base = escapeHTML(assetBase);
  return `<img class="scene-image" src="${base}/${design}-1536.webp" srcset="${base}/${design}-768.webp 768w, ${base}/${design}-1536.webp 1536w" sizes="(max-width: 767px) 100vw, ${['orbital','arcade','neon','editorial'].includes(design) ? '100vw' : '55vw'}" width="1536" height="1024" alt="" fetchpriority="high" decoding="async">`;
}
export const browse = (label = '浏览商品') => `<button type="button" class="catalog-browse" data-catalog-jump>${escapeHTML(label)} <span aria-hidden="true">↗</span></button>`;
export const search = () => `<label class="catalog-search"><span class="sr-only">搜索商品</span><input class="search" type="search" placeholder="搜索你需要的商品…" aria-label="搜索商品" autocomplete="off"><span class="search-mark" aria-hidden="true">⌕</span></label>`;
export function filters(categories = []) {
  return `<div class="filters" aria-label="商品分类"><button type="button" class="filter active" data-category="" aria-pressed="true"><span class="category-symbol" aria-hidden="true">✳</span>全部商品</button>${categories.map((c,i) => `<button type="button" class="filter" data-category="${escapeHTML(c.id)}" aria-pressed="false"><span class="category-symbol" aria-hidden="true">${['◈','✦','⊞','◇'][i%4]}</span>${escapeHTML(c.name)}</button>`).join('')}</div>`;
}
export function catalogue(categories, {title = '精选商品', hasSearch = true, style = ''} = {}) {
  return `<section class="catalogue ${style}" data-catalog><div class="catalog-heading"><h2>${escapeHTML(title)}</h2>${hasSearch ? search() : '<a href="#/orders">我的订单 ↗</a>'}</div>${filters(categories)}<div class="catalog-result"><span data-catalog-count role="status" aria-live="polite"></span><span>价格与交付方式以商品说明为准</span></div><section class="products" aria-label="商品列表"></section></section>`;
}
export const help = `<aside class="catalog-help"><div><h2>好工具，也需要好好了解。</h2><p>购买前了解适用范围、交付方式与售后说明。</p></div><a href="#/support">帮助与售后 <span aria-hidden="true">↗</span></a></aside>`;
export const title = (config, fallback) => escapeHTML(config?.heroTitle || fallback).replaceAll('\n','<br>');
