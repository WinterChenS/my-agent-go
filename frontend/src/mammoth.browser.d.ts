/**
 * The browser build of mammoth, which has no type declarations of its own.
 *
 * Only the one call the preview pane makes is described here: a `.docx`
 * ArrayBuffer in, the document's markup out.
 */
declare module 'mammoth/mammoth.browser' {
  export interface MammothMessage {
    type: string
    message: string
  }

  export interface MammothResult {
    value: string
    messages: MammothMessage[]
  }

  export function convertToHtml(input: { arrayBuffer: ArrayBuffer }): Promise<MammothResult>
}
