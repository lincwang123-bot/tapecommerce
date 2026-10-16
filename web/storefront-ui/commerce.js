// SPDX-License-Identifier: GPL-3.0-only
// Shared commerce presentation; raw *HTML arguments must be authored by callers.
import {escapeHTML as e} from './shared.js';
export const commerceHeading = (title, description = '', actionHTML = '') => `<div class="heading"><div><h1>${e(title)}</h1><p>${e(description)}</p></div>${actionHTML}</div>`;
export const commerceColumns = (detailsHTML, purchaseHTML) => `<div class="split"><section class="panel">${detailsHTML}</section><section class="panel">${purchaseHTML}</section></div>`;
export function purchaseConfirmation({title, quantity = 1, listPrice, total, currency = 'USDT', noteHTML = '', methodsHTML = '', actionsHTML = '', heading = '确认订单', totalLabel = '订单确认金额'}) {
  return `<h2>${e(heading)}</h2><p>${e(title)} × ${e(quantity)}</p>${listPrice ? `<p>${e(listPrice)}</p>` : ''}${noteHTML}<p>${e(totalLabel)}</p><div class="amount">${e(total)} <small>${e(currency)}</small></div>${methodsHTML}${actionsHTML}`;
}
export const orderSummary = ({statusHTML, dateHTML = '', total, currency = 'USDT', itemsHTML = ''}) => `<div class="panel"><div class="row">${statusHTML}<span class="muted">${dateHTML}</span></div><div class="amount">${e(total)} <small>${e(currency)}</small></div>${itemsHTML}</div>`;
