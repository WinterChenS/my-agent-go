/**
 * The thread follows a new answer until the reader takes the wheel back.
 *
 * These are the small, testable pieces behind that rule. Keeping them out of
 * the component matters: jsdom has no layout, so the component's effects can
 * only be trusted if the arithmetic they do has been checked on its own.
 */

/** A reader within this many pixels of the bottom is still following. */
export const FOLLOW_THRESHOLD = 40

export function bottomOffset(node: HTMLElement): number {
  return Math.max(0, node.scrollHeight - node.clientHeight)
}

export function distanceFromBottom(node: HTMLElement): number {
  return Math.max(0, node.scrollHeight - node.clientHeight - node.scrollTop)
}

export function isNearBottom(node: HTMLElement, threshold = FOLLOW_THRESHOLD): boolean {
  return distanceFromBottom(node) <= threshold
}

/**
 * Where to put the scroller so `child` starts at the top of the viewport.
 * Clamped because a child near the end cannot be moved higher than the bottom.
 */
export function topOffsetFor(node: HTMLElement, child: HTMLElement): number {
  const view = node.getBoundingClientRect()
  const box = child.getBoundingClientRect()
  return Math.max(0, Math.min(bottomOffset(node), node.scrollTop + box.top - view.top))
}

/**
 * A scroll event is either ours (we just wrote `scrollTop`) or the reader's.
 * Comparing against the write we made is more reliable than a timer: a slow
 * webview can deliver our event after the timeout has already expired.
 */
export function isOurScroll(node: HTMLElement, expected: number | null): boolean {
  return expected !== null && Math.abs(node.scrollTop - expected) <= 2
}

/**
 * Height the invisible tail must have so the furthest reachable scroll lands
 * the newest question at the top of the viewport.
 *
 * A freshly sent message has almost nothing under it: the scroller runs out of
 * content and clamps, so the question cannot reach the top. `targetTop` is the
 * scroll position that would put it there; `scrollHeight - clientHeight` is the
 * furthest we can currently go. Adding their difference to the tail makes the
 * two agree. The value is scroll-position independent, so recomputing it every
 * render converges in one step and never fights the reader's own scrolling.
 */
export function tailHeightFor(
  targetTop: number,
  scrollHeight: number,
  clientHeight: number,
  currentTail: number,
): number {
  const reachable = scrollHeight - clientHeight
  return Math.max(0, currentTail + (targetTop - reachable))
}
