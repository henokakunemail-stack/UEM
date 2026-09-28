import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const root = join(dirname(fileURLToPath(import.meta.url)), '..')

// Class names assembled at runtime from a value, so no literal `className` in
// the source ever names the full token. Each entry is a family: `prefix` is what
// the scanner actually extracted (`toast-` from `toast-${type}`), and `probes`
// list the finite set of classes that family can produce. A family is waived
// only when EVERY probe resolves, so a fifth toast variant with no rule behind
// it still fails.
const RUNTIME_BUILT = {
  'toast-': ['toast-card.toast-success', 'toast-card.toast-error', 'toast-card.toast-warning', 'toast-card.toast-info'],
  'log-line-': ['log-line-error', 'log-line-warn', 'log-line-info', 'log-line-debug'],
  'ui-modal-': ['ui-modal-sm', 'ui-modal-md', 'ui-modal-lg', 'ui-modal-xl', 'ui-modal-full'],
}

export function loadTs() {
  return require(join(root, 'node_modules', 'typescript'))
}

/**
 * Every class literal reachable from a JSX className attribute: plain strings,
 * template quasis, and the string arms of conditionals inside the expression.
 * An interpolated value is a class the scanner cannot see, so it is skipped —
 * but its branches are still walked and the literal quasis around it are kept.
 * A className that is purely a value (a variable, a function result) yields
 * nothing, which is correct: it is not this checker's business.
 */
export function collectClassNames(ts, source, fileName = 'source.tsx') {
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const found = new Set()

  const collect = (node) => {
    if (ts.isJsxExpression(node)) {
      // className={...} — look inside the expression container.
      collect(node.expression)
    } else if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
      for (const t of node.text.split(/\s+/)) if (t) found.add(t)
    } else if (ts.isTemplateLiteralToken(node)) {
      // The literal pieces of a template expression: head, middles and tail.
      for (const t of node.text.split(/\s+/)) if (t) found.add(t)
    } else if (ts.isTemplateExpression(node)) {
      collect(node.head)
      for (const span of node.templateSpans) {
        collect(span.literal)
        collect(span.expression)
      }
    } else if (ts.isConditionalExpression(node)) {
      collect(node.whenTrue)
      collect(node.whenFalse)
    } else if (ts.isBinaryExpression(node)) {
      const op = node.operatorToken.kind
      if (op === ts.SyntaxKind.AmpersandAmpersandToken || op === ts.SyntaxKind.BarBarToken) {
        collect(node.left)
        collect(node.right)
      }
    } else if (ts.isParenthesizedExpression(node)) {
      collect(node.expression)
    }
  }

  const visit = (node) => {
    if (ts.isJsxAttribute(node) && node.name.text === 'className' && node.initializer) {
      collect(node.initializer)
    }
    ts.forEachChild(node, visit)
  }

  visit(sf)
  return found
}

/** A compound probe such as `toast-card.toast-success` holds only if every part does. */
export function probeResolves(probe, defined) {
  return probe.split('.').every((part) => defined.has(part))
}

export function findMissing(ts, source, fileName, defined) {
  const missing = new Map()
  for (const t of collectClassNames(ts, source, fileName)) {
    // Not a plausible class token (an interpolation remnant, a stray brace).
    if (!/^[A-Za-z][^\s]*$/.test(t)) continue
    // A dashed utility is also emitted in camelCase form by Tailwind.
    const camel = t.replace(/-([a-z0-9])/g, (_, c) => c.toUpperCase())
    if (defined.has(t) || defined.has(camel)) continue
    if (!missing.has(t)) missing.set(t, new Set())
    missing.get(t).add(relative(root, fileName).split('\\').join('/'))
  }
  return missing
}

// --- CLI ---------------------------------------------------------------
// Imported by the self-check for the extractor, so the scan only runs when this
// file is the entry point.
export function main() {
  // vite.config.ts points outDir at the Go embed directory, so that is where the
  // bundle actually lands. Reading web-console/dist here would check a directory
  // nothing writes to any more — and a stale one that would still "pass".
  const distAssets = join(root, '..', 'server', 'cmd', 'server', 'dist', 'assets')
  let cssFile
  try {
    cssFile = readdirSync(distAssets).find((f) => f.endsWith('.css'))
  } catch {
    cssFile = undefined
  }
  if (!cssFile) {
    console.error('No build output in server/cmd/server/dist/assets. Run `npm run build` first.')
    process.exit(2)
  }

  const css = readFileSync(join(distAssets, cssFile), 'utf-8')
  // Match both `.name` and escaped forms like `.md\:inline` and `.w-\[85vh\]`.
  const defined = new Set(
    [...css.matchAll(/\.((?:[a-zA-Z0-9_-]|\\.)+)/g)].map((m) => m[1].replace(/\\/g, ''))
  )

  const files = []
  const walk = (d) => {
    for (const e of readdirSync(d)) {
      const p = join(d, e)
      if (statSync(p).isDirectory()) walk(p)
      else if (p.endsWith('.tsx')) files.push(p)
    }
  }
  walk(join(root, 'src'))

  const ts = loadTs()
  const missing = new Map()
  for (const f of files) {
    for (const [t, fs] of findMissing(ts, readFileSync(f, 'utf-8'), f, defined)) {
      if (!missing.has(t)) missing.set(t, new Set())
      for (const s of fs) missing.get(t).add(s)
    }
  }

  const real = []
  for (const [t, fs] of missing) {
    const family = Object.keys(RUNTIME_BUILT).find((prefix) => t.startsWith(prefix))
    if (family && RUNTIME_BUILT[family].every((p) => probeResolves(p, defined))) continue
    real.push([t, fs])
  }

  if (real.length) {
    console.error(`FAIL: ${real.length} class name(s) resolve to no CSS rule:\n`)
    for (const [t, fs] of real.sort()) {
      console.error(`  .${t.padEnd(24)} used in ${[...fs].join(', ')}`)
    }
    console.error(
      '\nEither the class is misspelled, or a stylesheet rule is missing. ' +
        'If it is built from a runtime value, add a family entry to RUNTIME_BUILT above.'
    )
    process.exit(1)
  }

  console.log(`OK: every className in ${files.length} components resolves to a real CSS rule (${cssFile})`)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
