import {heroImage, browse, catalogue, help, title, escapeHTML} from '../shared.js';
export function arcade(model) {
  return `<section class="arcade-hero">${heroImage('arcade',model)}<div class="arcade-sign"><p>${escapeHTML(model.brand || '数字商店')}</p><h1>${title(model,'装备你的数字世界！')}</h1></div><div class="arcade-action">${browse('开始选购')}</div></section>${catalogue(model.categories,{title:'发现你的新装备'})}${help}`;
}
