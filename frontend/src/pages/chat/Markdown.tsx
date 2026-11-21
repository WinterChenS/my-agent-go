import { Children, isValidElement, useMemo, useRef, useState, type ReactNode } from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useI18n } from '../../i18n/LanguageProvider'
import { copyText } from './copyText'

/** Stable plugin list: a new array on each render re-parses the whole tree. */
const REMARK_PLUGINS = [remarkGfm]

/**
 * `[n]` markers in an agent message become buttons that open the sources pane.
 *
 * The count comes from the same rail read that produced the numbers, so a
 * bracket whose number has no source stays literal text: linking to a source
 * that does not exist would be worse than showing the bracket.
 */
export function renderCites(text: string, sources: number, onCite: () => void): ReactNode {
  if (sources === 0 || !text.includes('[')) return text
  const nodes: ReactNode[] = []
  const pattern = /\[(\d{1,3})\]/g
  let cursor = 0
  let match: RegExpExecArray | null = pattern.exec(text)
  while (match !== null) {
    const number = Number(match[1])
    if (number >= 1 && number <= sources) {
      if (match.index > cursor) nodes.push(text.slice(cursor, match.index))
      nodes.push(
        <button
          key={`${number}-${match.index}`}
          type="button"
          className="chat-cite"
          data-testid="chat-cite"
          data-number={number}
          aria-label={`[${number}]`}
          onClick={() => {
            console.log('CITE_CLICK')
            onCite()
          }}
        >
          {number}
        </button>,
      )
      cursor = match.index + match[0].length
    }
    match = pattern.exec(text)
  }
  if (nodes.length === 0) return text
  if (cursor < text.length) nodes.push(text.slice(cursor))
  return <>{nodes}</>
}

/** Markdown children are strings mixed with elements; only strings can hold a cite. */
function withCites(children: ReactNode, sources: number, onCite: () => void): ReactNode {
  if (sources === 0) return children
  return Children.map(children, (child) =>
    typeof child === 'string' && child.includes('[') ? renderCites(child, sources, onCite) : child,
  )
}

/** Plain text of a React subtree, used by the code-block copy button. */
function textOf(node: ReactNode): string {
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(textOf).join('')
  if (isValidElement<{ children?: ReactNode }>(node)) return textOf(node.props.children)
  return ''
}

/**
 * The conversation transcript is Markdown, not flat text. `react-markdown`
 * builds React elements (never `innerHTML`) and `remark-gfm` adds tables,
 * task lists, strikethrough and autolinks. Raw HTML in a model reply stays
 * inert and `javascript:` URLs are dropped by the default URL transform.
 */
export function Markdown({
  text,
  sources = 0,
  onCite,
}: {
  text: string
  sources?: number
  onCite?: () => void
}) {
  // The transcript re-renders whenever the rail, a tool card or a reasoning
  // block updates. A fresh component map on every render made react-markdown
  // rebuild its subtree, which detached a `[n]` button exactly as it was being
  // clicked. The map is keyed on `sources` only and reads the callback through
  // a ref, so the generated DOM survives unrelated parent renders.
  const citeRef = useRef(onCite)
  citeRef.current = onCite

  const components = useMemo<Components>(() => {
    const cite = (children: ReactNode) => withCites(children, sources, () => citeRef.current?.())
    return {
      p: ({ children }) => <p>{cite(children)}</p>,
      h1: ({ children }) => <h1>{cite(children)}</h1>,
      h2: ({ children }) => <h2>{cite(children)}</h2>,
      h3: ({ children }) => <h3>{cite(children)}</h3>,
      h4: ({ children }) => <h4>{cite(children)}</h4>,
      h5: ({ children }) => <h5>{cite(children)}</h5>,
      h6: ({ children }) => <h6>{cite(children)}</h6>,
      li: ({ children }) => <li>{cite(children)}</li>,
      strong: ({ children }) => <strong>{cite(children)}</strong>,
      em: ({ children }) => <em>{cite(children)}</em>,
      del: ({ children }) => <del>{cite(children)}</del>,
      td: ({ children }) => <td>{cite(children)}</td>,
      th: ({ children }) => <th>{cite(children)}</th>,
      blockquote: ({ children }) => <blockquote>{cite(children)}</blockquote>,
      a: ({ href, children }) => (
        <a href={href} target="_blank" rel="noreferrer noopener">
          {cite(children)}
        </a>
      ),
      img: ({ src, alt }) => (
        <img src={typeof src === 'string' ? src : undefined} alt={alt ?? ''} loading="lazy" />
      ),
      table: ({ children }) => (
        <div className="chat-table-wrap">
          <table>{children}</table>
        </div>
      ),
      pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
    }
  }, [sources])

  return (
    <ReactMarkdown remarkPlugins={REMARK_PLUGINS} components={components}>
      {text}
    </ReactMarkdown>
  )
}

/** A fenced block with its language and a copy button, instead of a bare <pre>. */
function CodeBlock({ children }: { children: ReactNode }) {
  const { t } = useI18n()
  const [copied, setCopied] = useState(false)
  const code = Children.toArray(children)[0]
  const className = isValidElement<{ className?: string }>(code) ? code.props.className ?? '' : ''
  const language = /language-([\w+-]+)/.exec(className)?.[1] ?? ''
  const body = textOf(children)

  const copy = () => {
    void copyText(body).then((ok) => {
      if (!ok) return
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1600)
    })
  }

  return (
    <div className="chat-code-block" data-language={language || 'text'}>
      <div className="chat-code-head">
        <span className="chat-code-lang">{language || 'text'}</span>
        <button type="button" className="chat-code-copy" onClick={copy} aria-label={t('chat.markdown.copy')}>
          {copied ? t('chat.markdown.copied') : t('chat.markdown.copy')}
        </button>
      </div>
      <pre>{children}</pre>
    </div>
  )
}
