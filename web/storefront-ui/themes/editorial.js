import {heroImage, browse, catalogue, help, title, escapeHTML} from '../shared.js';
export function editorial(model) {
  return `<section class="editorial-hero"><aside class="editorial-margin"><p class="editorial-wordmark">DIGITAL<br>GOODS</p><p>${escapeHTML(model.brand || '数字商店')}<br><span>Tools for a wider world.</span></p><a href="#/support">发现更多可能 ↗</a></aside><div class="editorial-scene">${heroImage('editorial',model)}<div class="editorial-copy"><h1>${title(model,'工具之外，\n是更大的世界。')}</h1><p class="catalog-deck">给想法一个开始，给自己更多空间。</p>${browse()}</div></div></section>${catalogue(model.categories,{title:'好工具，始于选择'})}${help}`;
}
