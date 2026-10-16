import {heroImage, search, catalogue, help, title} from '../shared.js';
export function warm(model) {
  return `<section class="warm-hero"><div class="hero-copy"><p class="catalog-eyebrow">MAKE ROOM FOR INSPIRATION</p><h1>${title(model,'好工具，\n让生活更有灵感。')}</h1><p class="catalog-deck">工作里的小帮手，生活里的新灵感。</p>${search()}</div><div class="warm-scene">${heroImage('warm',model)}</div></section>${catalogue(model.categories,{title:'发现日常好物',hasSearch:false})}${help}`;
}
