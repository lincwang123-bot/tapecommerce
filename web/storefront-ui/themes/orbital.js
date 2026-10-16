import {heroImage, search, catalogue, help, title} from '../shared.js';
export function orbital(model) {
  return `<section class="orbital-hero">${heroImage('orbital',model)}<div class="orbital-copy"><p class="catalog-eyebrow">A WORLD OF POSSIBILITIES</p><h1>${title(model,'连接全球\n数字生产力')}</h1><p class="catalog-deck">更好的工具，更多可能。</p>${search()}</div></section>${catalogue(model.categories,{title:'探索数字工具',hasSearch:false})}${help}`;
}
