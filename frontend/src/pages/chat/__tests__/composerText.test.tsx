import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import {
  chipEditPosition,
  InlineDraft,
  insertSkill,
  skillAtCaret,
  skillRanges,
  withoutRange,
} from '../composerText'

afterEach(() => cleanup())

const paths = new Map([
  ['code-review', 'D:/skills/code-review.md'],
  ['release-notes', 'D:/skills/release-notes.md'],
])
const pathOf = (name: string) => paths.get(name)

describe('composer inline skills', () => {
  it('finds known skill tokens with their character spans', () => {
    const text = '看 @code-review 和 @release-notes，@unknown 不算'
    expect(skillRanges(text, pathOf)).toEqual([
      { name: 'code-review', path: 'D:/skills/code-review.md', start: 2, end: 14 },
      { name: 'release-notes', path: 'D:/skills/release-notes.md', start: 17, end: 31 },
    ])
  })

  it('requires a word boundary so paths and dates are not skills', () => {
    expect(skillRanges('src/@code-review', pathOf)).toEqual([])
    expect(skillRanges('11/16', pathOf)).toEqual([])
    expect(skillRanges('@code-review,', pathOf)).toEqual([
      { name: 'code-review', path: 'D:/skills/code-review.md', start: 0, end: 12 },
    ])
  })

  it('replaces the trigger token with the skill and reports the caret', () => {
    expect(insertSkill('/code', 'code-review', 'slash')).toEqual({
      value: '@code-review ',
      caret: 13,
    })
    expect(insertSkill('看 /code', 'code-review', 'slash')).toEqual({
      value: '看 @code-review ',
      caret: 15,
    })
    expect(insertSkill('@code', 'code-review', 'at')).toEqual({
      value: '@code-review ',
      caret: 13,
    })
    // A pick with no token in front still lands at the end, never glued on.
    expect(insertSkill('看', 'code-review', 'at')).toEqual({
      value: '看 @code-review ',
      caret: 15,
    })
  })

  it('removes a block and its separator when Backspace is pressed after it', () => {
    const text = '看 @code-review 和 @release-notes '
    const ranges = skillRanges(text, pathOf)
    const second = ranges[1]
    const hit = skillAtCaret(text, ranges, text.length, 'back')
    expect(hit?.name).toBe('release-notes')
    expect(withoutRange(text, hit ?? second)).toEqual({
      value: '看 @code-review 和 ',
      caret: 17,
    })

    // A Backspace inside the block takes the whole block, not one character.
    const inside = skillAtCaret(text, ranges, 5, 'back')
    expect(inside?.name).toBe('code-review')
    // Immediately before the `@` it is an ordinary character delete.
    expect(skillAtCaret(text, ranges, 2, 'back')).toBeUndefined()
  })

  it('detects edits that would rename a chip', () => {
    const before = '@code-review '
    const ranges = skillRanges(before, pathOf)
    expect(chipEditPosition(before, '@coxxde-review ', ranges)).toBe(3)
    expect(chipEditPosition(before, '@code-review hello', ranges)).toBeNull()
    expect(chipEditPosition(before, '@code-review', ranges)).toBeNull()
  })

  it('paints the block behind the same characters', () => {
    const { container } = render(<InlineDraft text="用 @code-review 看下" pathOf={pathOf} />)
    const block = container.querySelector('.chat-inline-skill')
    expect(block).toHaveAttribute('data-skill', 'code-review')
    expect(block).toHaveTextContent('@code-review')
    expect(container.textContent).toContain('用 @code-review 看下')
    // An unknown token stays plain text.
    const { container: plain } = render(<InlineDraft text="用 @unknown 看下" pathOf={pathOf} />)
    expect(plain.querySelector('.chat-inline-skill')).toBeNull()
  })
})
