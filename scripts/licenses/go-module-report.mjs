#!/usr/bin/env node
// Reads the concatenated JSON stream produced by `go list -deps -json`
// and emits a CSV with one row per non-self, non-standard Go module,
// resolving the license name from the LICENSE/COPYING file inside the
// module directory and the license URL from the upstream source path.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

const stream = readFileSync(0, 'utf8');

const modules = new Map();
let depth = 0;
let start = 0;
for (let i = 0; i < stream.length; i++) {
  const ch = stream[i];
  if (ch === '{') {
    if (depth === 0) start = i;
    depth++;
  } else if (ch === '}') {
    depth--;
    if (depth === 0) {
      const obj = JSON.parse(stream.slice(start, i + 1));
      if (obj && obj.Module && obj.Module.Path && !obj.Standard) {
        const m = obj.Module;
        if (!modules.has(m.Path)) modules.set(m.Path, m);
      }
    }
  }
}

const LICENSE_NAMES = new Set([
  'LICENSE', 'LICENSE.md', 'LICENSE.txt', 'LICENSE.rst',
  'COPYING', 'COPYING.md', 'License',
]);

const SPDX_HINTS = [
  ['MIT', /\bMIT License\b|\bPermission is hereby granted, free of charge\b/i],
  ['Apache-2.0', /\bApache License\b|Version 2\.0/i],
  ['ISC', /\bPermission to use, copy, modify, and(?:\/or)? distribute\b/i, /\bISC License\b/i],
  ['MPL-2.0', /Mozilla Public License/i],
  ['LGPL-3.0', /GNU LESSER GENERAL PUBLIC LICENSE\s+Version 3/i],
  ['LGPL-2.1', /GNU LESSER GENERAL PUBLIC LICENSE\s+Version 2\.1/i],
  ['GPL-3.0', /GNU General Public License.*(?:Version 3|version 3)/is],
  ['GPL-2.0', /GNU General Public License/i],
  ['Unlicense', /\bThis is free and unencumbered software released into the public domain\b/i],
];

function inferBSD(body) {
  if (!/Redistribution and use in source and binary forms/i.test(body)
      || !/Redistributions? of source code must retain/i.test(body)
      || !/Redistributions? in binary form must reproduce/i.test(body)
      || !/THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS? AND CONTRIBUTORS? "?AS IS"?/i.test(body)) {
    return null;
  }
  return /Neither the name of|Neither the names? of|may be used to endorse or promote/i.test(body)
    ? 'BSD-3-Clause'
    : 'BSD-2-Clause';
}

function inferLicense(moduleDir) {
  if (!moduleDir) return { type: 'Unknown', url: '' };
  let entries;
  try {
    entries = readdirSync(moduleDir);
  } catch {
    return { type: 'Unknown', url: '' };
  }
  const licenseFile = entries.find((name) => LICENSE_NAMES.has(name));
  if (!licenseFile) {
    return { type: 'Unknown', url: '', file: '' };
  }
  const filePath = join(moduleDir, licenseFile);
  let body = '';
  try {
    const stats = statSync(filePath);
    if (stats.isFile() && stats.size <= 256 * 1024) {
      body = readFileSync(filePath, 'utf8');
    }
  } catch {
    /* fall through */
  }
  const normalizedBody = body.replace(/\s+/g, ' ');
  let type = inferBSD(normalizedBody);
  if (!type) {
    for (const [name, ...patterns] of SPDX_HINTS) {
      if (patterns.some((re) => re.test(normalizedBody))) {
        type = name;
        break;
      }
    }
  }
  return { type: type || 'Unknown', url: '', file: licenseFile };
}

function inferUrl(path, version) {
  const suffix = version ? `@${encodeURIComponent(version)}` : '';
  return `https://pkg.go.dev/${path}${suffix}?tab=licenses`;
}

const rows = [];
const failures = [];
for (const [path, mod] of modules) {
  if (path === 'github.com/kingsunb/NovaVeil') continue;
  const { type, file } = inferLicense(mod.Dir || '');
  const url = inferUrl(path, mod.Version);
  if (!file || type === 'Unknown') {
    failures.push(`${path}@${mod.Version || 'unknown'}: ${!file ? 'license file not found' : 'license text is not recognized'}`);
    continue;
  }
  rows.push([path, url, type]);
}

if (failures.length > 0) {
  console.error(`Go license inventory failed:\n${failures.join('\n')}`);
  process.exit(1);
}

rows.sort((a, b) => a[0].localeCompare(b[0]));

const out = process.stdout;
for (const [path, url, type] of rows) {
  const safe = (s) => `"${String(s).replace(/"/g, '""')}"`;
  out.write(`${safe(path)},${safe(url)},${safe(type)}\n`);
}
