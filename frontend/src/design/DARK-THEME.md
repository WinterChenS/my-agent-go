# Dark theme derivation

The prototype only ships a light baseline. The dark theme keeps every
non-colour token unchanged (radii, spacing, type scale, fonts, shadows,
breakpoints) and only remaps the semantic colour tokens in
`tokens.css` under `[data-theme='dark']`.

## Hierarchy

| Role | OKLCH | Relative luminance |
| --- | --- | --- |
| background | `oklch(16% 0.012 255)` | 0.004 |
| surface | `oklch(20% 0.014 255)` | 0.008 |
| surface-2 | `oklch(23% 0.014 255)` | 0.012 |
| surface-3 | `oklch(27% 0.016 255)` | 0.020 |
| foreground | `oklch(95% 0.005 250)` | 0.858 |
| foreground-2 | `oklch(80% 0.008 250)` | 0.513 |
| muted | `oklch(65% 0.01 250)` | 0.275 |
| border | `oklch(30% 0.012 255)` | 0.027 |
| border-strong | `oklch(40% 0.014 255)` | 0.064 |
| accent | `oklch(70% 0.15 255)` | 0.342 |

Surfaces are separated by ~0.004–0.008 luminance steps so cards remain
distinguishable on OLED/IPS panels without relying on borders alone.

## Contrast (WCAG 2.1 relative luminance)

| Pair | Ratio |
| --- | --- |
| foreground / background | 16.8:1 |
| foreground-2 / background | 10.4:1 |
| muted / background | 6.0:1 |
| accent / background | 7.2:1 |
| on-accent / accent | 7.2:1 |
| success / background | 8.2:1 |
| warn / background | 9.6:1 |
| danger / background | 6.8:1 |

Borders are intentionally low-contrast non-text separators (1.4:1 / 2.1:1).

## Shadow structure

Shadow geometry is unchanged. `--shadow-color` is the only colour input; it
is `var(--fg)` in light mode and near-black in dark mode, so the same shadow
structure remains visible instead of becoming a white glow.
