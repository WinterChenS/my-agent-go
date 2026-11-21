import { useEffect, useState } from 'react'
import { previewFile, type FilePreview } from '../../chat/client'
import { useI18n } from '../../i18n/LanguageProvider'
import Icon from '../../shell/Icon'
import { Markdown } from './Markdown'

/**
 * One written file, shown inside the right pane.
 *
 * The two entry points (the file list under an answer and the output list in
 * the pane) both fill this pane, so a click never covers the conversation it
 * came from: a dialog took the whole window for a file the reader wanted to
 * glance at next to the answer that produced it.
 *
 * Heavy parsers load on demand: a spreadsheet or a Word document pulls its
 * library when it is actually opened, not into the chat bundle.
 */
export function FilePreviewPane({
  sessionId,
  path,
  onClose,
}: {
  sessionId: string
  path: string
  onClose: () => void
}) {
  const { t } = useI18n()
  const [preview, setPreview] = useState<FilePreview | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let live = true
    setPreview(null)
    setError('')
    void previewFile(sessionId, path)
      .then((file) => {
        if (live) setPreview(file)
      })
      .catch((cause) => {
        if (live) setError(cause instanceof Error ? cause.message : String(cause))
      })
    return () => {
      live = false
    }
  }, [sessionId, path])

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const name = preview?.name ?? path.split(/[\\/]/).pop() ?? path

  return (
    <section className="file-preview" data-testid="file-preview" aria-label={name}>
        <header className="file-preview-head">
          <span className="file-preview-icon" aria-hidden="true">
            <Icon name="file" size={14} />
          </span>
          <span className="file-preview-copy">
            <b data-testid="file-preview-name">{name}</b>
            <small className="mono">{preview ? `${size(preview.bytes)} · ${preview.kind}` : ''}</small>
          </span>
          <button
            type="button"
            className="chat-iconbtn"
            aria-label={t('chat.preview.close')}
            data-testid="file-preview-close"
            onClick={onClose}
          >
            <Icon name="x" size={14} />
          </button>
        </header>

        <div className="file-preview-body" data-testid="file-preview-body" data-kind={preview?.kind ?? ''}>
          {error ? (
            <p className="file-preview-note is-error" role="alert">
              {t('chat.preview.failed', { reason: error })}
            </p>
          ) : !preview ? (
            <p className="file-preview-note">{t('chat.preview.loading')}</p>
          ) : preview.tooLarge ? (
            <p className="file-preview-note">{t('chat.preview.tooLarge')}</p>
          ) : (
            <PreviewBody preview={preview} />
          )}
        </div>

        {preview?.truncated ? (
          <footer className="file-preview-foot">{t('chat.preview.truncated')}</footer>
        ) : null}
    </section>
  )
}

function PreviewBody({ preview }: { preview: FilePreview }) {
  const { t } = useI18n()
  const text = preview.text ?? ''

  if (preview.kind === 'image' && preview.data) {
    return <img className="file-preview-image" data-testid="file-preview-image" src={preview.data} alt={preview.name} />
  }
  if (preview.kind === 'pdf' && preview.data) {
    return <iframe className="file-preview-frame" title={preview.name} src={preview.data} data-testid="file-preview-pdf" />
  }
  if (preview.kind === 'html') {
    return (
      <iframe
        className="file-preview-frame"
        title={preview.name}
        data-testid="file-preview-html"
        // Rendered, not executed: the page is the file's own markup, and a
        // script inside it has no business running inside the app shell.
        sandbox=""
        srcDoc={text}
      />
    )
  }
  if (preview.kind === 'markdown') {
    return (
      <div className="file-preview-prose chat-markdown" data-testid="file-preview-markdown">
        <Markdown text={text} sources={0} onCite={() => undefined} />
      </div>
    )
  }
  if (preview.kind === 'table') {
    return <TablePreview preview={preview} />
  }
  if (preview.kind === 'word') {
    return <WordPreview preview={preview} />
  }
  if (preview.kind === 'binary') {
    return <p className="file-preview-note">{t('chat.preview.binary')}</p>
  }
  return (
    <pre className="file-preview-text" data-testid="file-preview-text">
      {text}
    </pre>
  )
}

/** A spreadsheet or a CSV, as a table. */
function TablePreview({ preview }: { preview: FilePreview }) {
  const { t } = useI18n()
  const [sheets, setSheets] = useState<{ name: string; rows: string[][] }[] | null>(null)
  const [active, setActive] = useState(0)
  const [error, setError] = useState('')

  useEffect(() => {
    let live = true
    const ext = preview.name.split('.').pop()?.toLowerCase() ?? ''
    if (ext === 'csv' || ext === 'tsv') {
      setSheets([{ name: preview.name, rows: parseDelimited(preview.text ?? '', ext === 'tsv' ? '\t' : ',') }])
      return
    }
    void import('xlsx')
      .then((XLSX) => {
        const raw = preview.data ?? ''
        const base64 = raw.slice(raw.indexOf(',') + 1)
        const workbook = XLSX.read(base64, { type: 'base64' })
        if (!live) return
        setSheets(
          workbook.SheetNames.slice(0, 8).map((name) => ({
            name,
            rows: XLSX.utils
              .sheet_to_json<unknown[]>(workbook.Sheets[name], { header: 1, raw: false, defval: '' })
              .slice(0, 500)
              .map((row) => row.map((cell) => String(cell ?? ''))),
          })),
        )
      })
      .catch((cause) => live && setError(String(cause)))
    return () => {
      live = false
    }
  }, [preview])

  if (error) return <p className="file-preview-note is-error">{t('chat.preview.failed', { reason: error })}</p>
  if (!sheets) return <p className="file-preview-note">{t('chat.preview.loading')}</p>
  const sheet = sheets[Math.min(active, sheets.length - 1)]
  const width = Math.max(1, ...sheet.rows.map((row) => row.length))
  return (
    <div className="file-preview-table-wrap">
      {sheets.length > 1 ? (
        <div className="file-preview-tabs" role="tablist">
          {sheets.map((entry, index) => (
            <button
              key={entry.name}
              type="button"
              role="tab"
              aria-selected={index === active}
              className={`chat-btn chat-btn-sm${index === active ? ' is-on' : ''}`}
              onClick={() => setActive(index)}
            >
              {entry.name}
            </button>
          ))}
        </div>
      ) : null}
      <table className="file-preview-table" data-testid="file-preview-table">
        <tbody>
          {sheet.rows.map((row, rowIndex) => (
            <tr key={rowIndex} className={rowIndex === 0 ? 'is-head' : ''}>
              {Array.from({ length: width }, (_, cellIndex) => (
                <td key={cellIndex}>{row[cellIndex] ?? ''}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** A Word document, converted to markup by mammoth on demand. */
function WordPreview({ preview }: { preview: FilePreview }) {
  const { t } = useI18n()
  const [html, setHtml] = useState<string | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let live = true
    void import('mammoth/mammoth.browser')
      .then((mammoth) => {
        const raw = preview.data ?? ''
        const binary = atob(raw.slice(raw.indexOf(',') + 1))
        const bytes = new Uint8Array(binary.length)
        for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
        return mammoth.convertToHtml({ arrayBuffer: bytes.buffer })
      })
      .then((result) => live && setHtml(result.value))
      .catch((cause) => live && setError(String(cause)))
    return () => {
      live = false
    }
  }, [preview])

  if (error) return <p className="file-preview-note is-error">{t('chat.preview.failed', { reason: error })}</p>
  if (html === null) return <p className="file-preview-note">{t('chat.preview.loading')}</p>
  // mammoth emits a fixed set of structural tags (p, h1-h6, ul, ol, li, table,
  // strong, em, a). Nothing else in the document survives the conversion.
  return (
    <div
      className="file-preview-prose chat-markdown"
      data-testid="file-preview-word"
      dangerouslySetInnerHTML={{ __html: html }}
    />
  )
}

/**
 * Split delimited text into rows.
 *
 * Quoted fields are honoured because a spreadsheet export with a comma inside a
 * title is the normal case, not an edge case.
 */
export function parseDelimited(text: string, delimiter: string): string[][] {
  const rows: string[][] = []
  let row: string[] = []
  let field = ''
  let quoted = false
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index]
    if (quoted) {
      if (char === '"' && text[index + 1] === '"') {
        field += '"'
        index += 1
      } else if (char === '"') {
        quoted = false
      } else {
        field += char
      }
      continue
    }
    if (char === '"') {
      quoted = true
    } else if (char === delimiter) {
      row.push(field)
      field = ''
    } else if (char === '\n') {
      row.push(field)
      rows.push(row)
      row = []
      field = ''
    } else if (char !== '\r') {
      field += char
    }
  }
  if (field !== '' || row.length > 0) {
    row.push(field)
    rows.push(row)
  }
  return rows.slice(0, 500)
}

function size(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`
  return `${bytes} B`
}
