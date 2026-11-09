# Anvil design tokens (frozen)

Source of truth:

- Prototype commit: `112fcae854b324d2e75fdf5c890083ef9c1d3d6e`
- `docs/ui/Agent-客户端产品原型/DESIGN-MANIFEST.json` (`tokens.required`)
- `docs/ui/Agent-客户端产品原型/assets/studio.css` — the `:root` token block
- `docs/ui/Agent-客户端产品原型/assets/pages.css` — component usage; it depends
  on the `studio.css` variables (`--bg`, `--surface`, `--fg`, `--border`,
  `--accent`, radii, shadows, fonts)

`pages.css` does not define the variables itself; the values were extracted from
the `studio.css` token block, deduplicated into the semantic groups below, and
extended with the prototype's spacing rhythm, type scale and motion timings.
Design-tool chrome (`studio.css` shell/editor sections that only frame the
export, and `studio.js` preview controls) is deliberately not part of the
product.

## Contract

- Shell and page code must reference these variables. Raw colour values
  (`#fff`, `rgb(...)`, `oklch(...)`) are rejected by the R0-01 token test.
- Do not substitute framework default colours, radii, shadows or fonts.
- `tokens.ts` mirrors the variable names for tests and components. Values remain
  in `tokens.css`.
- New tokens require an audit against the prototype and an update to this file.
