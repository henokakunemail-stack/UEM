// Self-check for the check-css extractor. Run: node scripts/check-css.test.mjs
import assert from 'node:assert/strict'
import { loadTs, collectClassNames, probeResolves } from './check-css.mjs'

const ts = loadTs()
const classes = (source) => [...collectClassNames(ts, source)].sort()

// Plain string attribute.
assert.deepEqual(classes('<div className="btn btn-primary" />'), ['btn', 'btn-primary'])

// Template literal: the interpolated value is a runtime class and is skipped,
// the literal quasis on both sides of it are kept.
assert.deepEqual(classes('<span className={`tab-btn ${tab} active`} />'), [
  'active',
  'tab-btn',
])

// Conditional inside a template: every string arm is a class the checker owns.
assert.deepEqual(
  classes(
    '<i className={`progress-bar-fill ${p > 85 ? "danger" : p > 70 ? "warning" : "primary"}`} />'
  ),
  ['danger', 'primary', 'progress-bar-fill', 'warning']
)

// Conditional expression as the whole value, plus empty arms.
assert.deepEqual(classes('<RefreshCw className={busy ? "spinning" : ""} />'), ['spinning'])

// && and || arms.
assert.deepEqual(classes('<div className={a && "row-selected"} />'), ['row-selected'])
assert.deepEqual(classes('<div className={a || "text-muted"} />'), ['text-muted'])

// Nested conditionals in the else arm.
assert.deepEqual(classes('<b className={x ? "ok" : y ? "warn" : "bad"} />'), [
  'bad',
  'ok',
  'warn',
])

// A value the scanner cannot see yields nothing: not this checker's business.
assert.deepEqual(classes('<div className={classesFor(device.status)} />'), [])
assert.deepEqual(classes('<div className={dynamic} />'), [])

// A custom component still forwards className to the DOM, so it is read too.
assert.deepEqual(classes('<Widget className="wrapper" />'), ['wrapper'])
assert.deepEqual(classes('<div data-x="not-a-class" className="kept" />'), ['kept'])

// A size suffix interpolated between a shared prefix and a static tail collects
// as the bare `ui-modal-`; the family probes are what confirm every size exists.
assert.deepEqual(classes('<dialog className={`ui-modal ui-modal-${size}`} />'), [
  'ui-modal',
  'ui-modal-',
])

// Compound probes hold only when every part of the compound selector exists.
assert.equal(probeResolves('toast-card.toast-success', new Set(['toast-card', 'toast-success'])), true)
assert.equal(probeResolves('toast-card.toast-error', new Set(['toast-card'])), false)

console.log('ok: check-css extraction self-check')
