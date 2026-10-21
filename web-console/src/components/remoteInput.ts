// Browser-to-endpoint input translation for the remote control session.

// Firefox reports a few keys with its own keyCodes. Sent raw, '-' arrived as
// VK_VOLUME_MUTE and muted the endpoint, and ';' and '=' typed nothing.
const FIREFOX_KEYCODES: Record<number, number> = {
  173: 189, // '-'
  59: 186, // ';'
  61: 187, // '='
  224: 91, // Meta/Cmd -> Windows key
}

/** The Windows virtual-key code for a browser key event, or 0 to send nothing
 *  (229 is an IME composition, which carries no key). */
export function virtualKeyCode(keyCode: number): number {
  if (keyCode === 229) return 0
  return FIREFOX_KEYCODES[keyCode] ?? keyCode
}

/** Turns a browser wheel event into whole notches, carrying the remainder, so a
 *  trackpad's stream of small deltas scrolls in proportion instead of one full
 *  notch per event. Returns the notches to send and the new remainder. */
export function wheelNotches(delta: number, deltaMode: number, carry: number): { notches: number; carry: number } {
  const px = deltaMode === 1 ? delta * 40 : deltaMode === 2 ? delta * 800 : delta
  const total = carry + px
  const notches = Math.trunc(total / 100)
  return { notches, carry: total - notches * 100 }
}
