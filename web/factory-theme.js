import {themes,themeFor} from './storefront-ui/registry.js';
export const FACTORY_TEMPLATES = Object.freeze(themes.map(t=>t.id));
export function applyFactoryTheme(config,doc=globalThis.document) {
 const theme=themeFor(config?.factory_theme?.template_id);
 doc.body.dataset.template=theme.id;
 doc.body.dataset.design=theme.design;
 doc.querySelector('meta[name="color-scheme"]')?.setAttribute('content',['orbital','neon'].includes(theme.design)?'dark':'light');
}
