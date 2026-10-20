// Verifies the console bundle the Go server embeds (server/cmd/server/dist).
//
// vite writes straight into the embed directory, so a stale bundle there is
// what the server ships. Two invariants:
//   1. every asset index.html references exists, or the console is broken;
//   2. every file in assets/ is reachable from index.html through the files
//      that reference it (entry -> lazy chunks -> their CSS), or it is a
//      leftover from an earlier build.
// An earlier check demanded exactly one JS and one CSS file. That stopped
// being true when pages became lazy-loaded chunks, and it failed every build.
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))

export function checkEmbed(dist) {
  const errors = []
  const indexPath = join(dist, 'index.html')
  if (!existsSync(indexPath)) return [`${indexPath} is missing`]
  const assetsDir = join(dist, 'assets')
  const files = existsSync(assetsDir) ? readdirSync(assetsDir) : []
  const names = new Set(files)

  const refsIn = (text) => {
    const out = new Set()
    for (const name of names) if (text.includes(name)) out.add(name)
    return out
  }

  const index = readFileSync(indexPath, 'utf-8')
  for (const m of index.matchAll(/\/assets\/([A-Za-z0-9._-]+)/g)) {
    if (!names.has(m[1])) errors.push(`index.html references assets/${m[1]}, which does not exist`)
  }

  const reached = new Set()
  const queue = [...refsIn(index)]
  while (queue.length) {
    const f = queue.pop()
    if (reached.has(f)) continue
    reached.add(f)
    if (/\.(js|css|mjs)$/.test(f)) {
      for (const r of refsIn(readFileSync(join(assetsDir, f), 'utf-8'))) if (!reached.has(r)) queue.push(r)
    }
  }
  for (const f of files) {
    if (!reached.has(f)) errors.push(`assets/${f} is not referenced from index.html or any chunk: a stale file from an earlier build`)
  }
  return errors
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const dist = resolve(here, '..', '..', 'server', 'cmd', 'server', 'dist')
  const errors = checkEmbed(dist)
  if (errors.length) {
    for (const e of errors) console.error(`::error::${e}`)
    process.exit(1)
  }
  console.log(`embed output verified: ${readdirSync(join(dist, 'assets')).length} assets, all reachable from index.html`)
}
