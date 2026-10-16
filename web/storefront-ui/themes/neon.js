import {heroImage, browse, catalogue, help, title} from '../shared.js';
export function neon(model) {
  return `<section class="neon-hero">${heroImage('neon',model)}<div class="neon-copy"><p class="neon-manifesto" aria-hidden="true">CREATE<br>YOUR NEXT.</p><h1>${title(model,'用更好的工具，\n创造更酷的自己。')}</h1><p class="catalog-deck">给灵感加一点电。</p>${browse()}</div></section>${catalogue(model.categories,{title:'你的下一件创作装备'})}${help}`;
}
