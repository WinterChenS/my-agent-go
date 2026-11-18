import type { ReactNode } from 'react'

/**
 * A skill the composer draft mentions inline. The draft keeps it as plain
 * `@name` text so the textarea's caret, selection and IME all keep working;
 * the mirror layer only paints a block behind those characters.
 */
export type SkillRange = {
  name: string
  path: string
  /** Index of the `@`, not of the name. */
  start: number
  /** Index just past the last character of the name. */
  end: number
}

/** Every `@name` that resolves to a real skill, with its character span. */
export function skillRanges(text: string, pathOf: (name: string) => string | undefined): SkillRange[] {
  const ranges: SkillRange[] = []
  // Punctuation ends a name: `@code-review,` still points at the skill.
  const pattern = /(^|\s)@([^\s@，。；：！？、,.;:!?]+)/g
  let match: RegExpExecArray | null = pattern.exec(text)
  while (match !== null) {
    const name = match[2]
    const path = pathOf(name)
    if (path) {
      const start = match.index + match[1].length
      ranges.push({ name, path, start, end: start + name.length + 1 })
    }
    match = pattern.exec(text)
  }
  return ranges
}

/**
 * The mirror layer paints only the skill blocks: its own text is transparent
 * and the real characters stay in the textarea above. Because the block adds
 * no padding, border or margin, both layers wrap at exactly the same place.
 */
export function InlineDraft({
  text,
  pathOf,
}: {
  text: string
  pathOf: (name: string) => string | undefined
}) {
  if (!text) return <span>{'\u200b'}</span>
  const ranges = skillRanges(text, pathOf)
  const nodes: ReactNode[] = []
  let cursor = 0
  for (const range of ranges) {
    if (range.start > cursor) nodes.push(text.slice(cursor, range.start))
    nodes.push(
      <span key={`${range.name}-${range.start}`} className="chat-inline-skill" data-skill={range.name}>
        {text.slice(range.start, range.end)}
      </span>,
    )
    cursor = range.end
  }
  if (cursor < text.length) nodes.push(text.slice(cursor))
  // A trailing zero-width space keeps the last line's height when the draft
  // ends with a newline, exactly like the textarea does.
  nodes.push('\u200b')
  return <>{nodes}</>
}

/**
 * Replace the token that opened the picker (`@query` or `/query`) with the
 * chosen skill and report where the caret belongs afterwards.
 */
export function insertSkill(
  text: string,
  name: string,
  trigger: 'at' | 'slash',
): { value: string; caret: number } {
  const token = trigger === 'slash' ? /\/[^\s]*$/ : /@[^\s]*$/
  const match = token.exec(text)
  const insertAt = match ? match.index : text.length
  const prefix = match ? text.slice(0, insertAt) : `${text}${text && !text.endsWith(' ') ? ' ' : ''}`
  const suffix = match ? text.slice(insertAt + match[0].length) : ''
  const value = `${prefix}@${name} ${suffix}`
  return { value, caret: prefix.length + name.length + 2 }
}

/**
 * The skill a Backspace (or Delete) at `caret` should remove as one block.
 *
 * A pick always leaves one space after the chip so the next word does not glue
 * itself to the name; Backspace from there takes the chip *and* that separator,
 * otherwise removing a block would need two presses.
 */
export function skillAtCaret(
  text: string,
  ranges: SkillRange[],
  caret: number,
  direction: 'back' | 'forward',
): SkillRange | undefined {
  if (direction === 'back') {
    const hit = ranges.find((range) => {
      const end = text[range.end] === ' ' ? range.end + 1 : range.end
      return caret > range.start && caret <= end
    })
    if (!hit) return undefined
    // Return the span including that separator so cutting it leaves no gap.
    return text[hit.end] === ' ' ? { ...hit, end: hit.end + 1 } : hit
  }
  return ranges.find((range) => caret >= range.start && caret < range.end)
}

/** Cut a whole block out of the draft, leaving the caret where it began. */
export function withoutRange(text: string, range: SkillRange): { value: string; caret: number } {
  return { value: text.slice(0, range.start) + text.slice(range.end), caret: range.start }
}

/**
 * Where an edit first differs from the previous draft, when that difference
 * lands inside a skill block. A chip must not be renameable: typing, pasting
 * or dragging into the middle of `@name` would silently point the turn at a
 * different skill (or at none at all), so the composer refuses those edits.
 */
export function chipEditPosition(
  before: string,
  after: string,
  ranges: SkillRange[],
): number | null {
  let index = 0
  while (index < before.length && index < after.length && before[index] === after[index]) index += 1
  return ranges.some((range) => index >= range.start && index < range.end) ? index : null
}
