import type { PiImage } from './client'

/** Only known raster types are safe to put in a data URI. */
const ALLOWED = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/bmp'])

/** A base64 image from the runtime, as an `<img>` source. */
export function imageSrc(image: PiImage): string {
  const mime = ALLOWED.has(image.mimeType) ? image.mimeType : 'image/png'
  return `data:${mime};base64,${image.data}`
}
