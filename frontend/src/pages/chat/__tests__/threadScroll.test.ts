import { describe, expect, it } from 'vitest'
import {
  bottomOffset,
  distanceFromBottom,
  FOLLOW_THRESHOLD,
  isNearBottom,
  isOurScroll,
  tailHeightFor,
  topOffsetFor,
} from '../threadScroll'

/** jsdom has no layout: give the scroller the geometry the helpers expect. */
function scroller(scrollTop: number, height: number, content: number) {
  const node = {
    scrollTop,
    clientHeight: height,
    scrollHeight: content,
    getBoundingClientRect: () => ({ top: 100 }),
  } as unknown as HTMLElement
  return node
}

function child(top: number) {
  return {
    getBoundingClientRect: () => ({ top }),
  } as unknown as HTMLElement
}

describe('thread follow arithmetic', () => {
  it('measures the distance from the bottom and clamps at zero', () => {
    const node = scroller(60, 100, 200)
    expect(bottomOffset(node)).toBe(100)
    expect(distanceFromBottom(node)).toBe(40)
    expect(distanceFromBottom(scroller(500, 100, 200))).toBe(0)
  })

  it('treats only the last few pixels as still following', () => {
    expect(isNearBottom(scroller(60, 100, 200))).toBe(true)
    expect(isNearBottom(scroller(50, 100, 200))).toBe(false)
    expect(isNearBottom(scroller(100 - FOLLOW_THRESHOLD, 100, 200))).toBe(true)
  })

  it('aligns a child to the top, clamped to the reachable range', () => {
    const node = scroller(0, 100, 500)
    expect(topOffsetFor(node, child(260))).toBe(160)
    // A child already above the viewport cannot be scrolled further up.
    expect(topOffsetFor(node, child(40))).toBe(0)
    // Nor past the end of the document.
    expect(topOffsetFor(scroller(0, 100, 500), child(900))).toBe(400)
  })

  it('tells our own scroll writes from the reader touching the wheel', () => {
    const node = scroller(160, 100, 500)
    expect(isOurScroll(node, 160)).toBe(true)
    expect(isOurScroll(node, 161)).toBe(true)
    expect(isOurScroll(node, 120)).toBe(false)
    expect(isOurScroll(node, null)).toBe(false)
  })

  it('sizes the tail so following the bottom pins the question at the top', () => {
    // The question sits 200px into the content but only 100px is reachable in
    // a 600px viewport; the tail must add the missing 100px.
    expect(tailHeightFor(200, 700, 600, 0)).toBe(100)
    // Recomputing on the next render leaves the tail unchanged (converged).
    expect(tailHeightFor(200, 800, 600, 100)).toBe(100)
    // When the answer is already taller than the screen, no tail is needed.
    expect(tailHeightFor(200, 1200, 600, 0)).toBe(0)
    // A shrinking requirement shrinks the tail back down, never below zero.
    expect(tailHeightFor(200, 900, 600, 100)).toBe(0)
  })
})
