// Checks the console's dictionaries, so a missing translation fails the build
// instead of showing up as English (or a raw key) in front of an Indonesian
// operator.
//
//   1. Every namespace file exists in every language.
//   2. Every language has exactly the keys English has, none empty.
//   3. A translation keeps every {placeholder} the English text has.
//   4. Every literal t('namespace.key') in src/ names a key that exists.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, dirname, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const localesDir = join(root, 'src', 'i18n', 'locales')
const langs = readdirSync(localesDir).filter((d) => statSync(join(localesDir, d)).isDirectory())
const errors = []

function load(lang) {
  const out = {}
  for (const file of readdirSync(join(localesDir, lang))) {
    if (!file.endsWith('.json')) continue
    const ns = file.slice(0, -5)
    const entries = JSON.parse(readFileSync(join(localesDir, lang, file), 'utf8'))
    for (const [k, v] of Object.entries(entries)) out[`${ns}.${k}`] = v
  }
  return out
}

const tables = Object.fromEntries(langs.map((l) => [l, load(l)]))
const en = tables.en
if (!en) {
  console.error('no English dictionary under src/i18n/locales/en')
  process.exit(1)
}

const placeholders = (s) => new Set([...String(s).matchAll(/\{(\w+)\}/g)].map((m) => m[1]))

for (const lang of langs) {
  if (lang === 'en') continue
  const t = tables[lang]
  for (const key of Object.keys(en)) {
    if (!(key in t)) errors.push(`${lang}: missing ${key}`)
    else if (typeof t[key] !== 'string' || t[key].trim() === '') errors.push(`${lang}: empty ${key}`)
    else {
      for (const p of placeholders(en[key])) {
        if (!placeholders(t[key]).has(p)) errors.push(`${lang}: ${key} lost the {${p}} placeholder`)
      }
    }
  }
  for (const key of Object.keys(t)) {
    if (!(key in en)) errors.push(`${lang}: ${key} has no English original`)
  }
}
for (const [key, v] of Object.entries(en)) {
  if (typeof v !== 'string' || v.trim() === '') errors.push(`en: empty ${key}`)
}

// Literal keys used in the source.
function walk(dir, files = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, files)
    else if (/\.(tsx?|jsx?)$/.test(name) && !/\.test\./.test(name)) files.push(p)
  }
  return files
}
const used = /\bt\(\s*(['"`])([a-zA-Z0-9_]+\.[a-zA-Z0-9_.]+)\1/g
for (const file of walk(join(root, 'src'))) {
  const src = readFileSync(file, 'utf8')
  for (const m of src.matchAll(used)) {
    if (!(m[2] in en)) errors.push(`${relative(root, file)}: t('${m[2]}') has no English text`)
  }
}

if (errors.length) {
  console.error(`i18n check failed (${errors.length}):\n  ` + errors.join('\n  '))
  process.exit(1)
}
console.log(`OK: ${Object.keys(en).length} keys in ${langs.join(', ')}; every t('...') resolves`)
