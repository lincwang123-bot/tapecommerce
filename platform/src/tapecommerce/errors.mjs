// SPDX-License-Identifier: GPL-3.0-only
// Shared domain errors without loading Studio config, database or UI modules.
export function fail(code, status = 400) {
  throw Object.assign(new Error(code), { code, status });
}
