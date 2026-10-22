# Design

The local dashboard (`internal/admin/style.go`, `internal/admin/templates.go`) follows the restraint of coss.com/ui, rebuilt in plain CSS: no front-end build, no client JavaScript, one embedded font.

## Principles

- Glance first. Each account answers "is it backing up normally?" with one status badge, one distribution bar and a few times. Everything else is folded into an accordion.
- Monochrome by default. Color appears only for state: green up to date, blue in progress, amber paused or verifying, red needs attention.
- Elevation is a tray and a sheet: a tinted, bordered frame (16px radius, 4px inset) holding a white sheet (12px radius, hairline border, 1px shadow). No other shadows, gradients or decorative fills.

## Tokens

All colors are CSS custom properties on `:root`, redefined under `prefers-color-scheme: dark`. Text uses `--fg` / `--fg-2` / `--muted` only. `--faint` is for borders and underlines, never text, because it fails contrast.

## Type

- Display: Cal Sans (OFL, Latin subset embedded and served at `/calsans.woff2`) for the brand, page titles, account names and fieldset legends.
- Body: the platform UI font at 14px; 13px for secondary text, 12px for labels.
- Numbers and times use tabular figures; codes use the platform monospace.

## Controls

- Buttons are 36px (32px `.small` in toolbars, 44px on coarse pointers), 8px radius. Primary is ink on paper; `.secondary` is bordered sheet; `.ghost` is text-only; `.danger` is a red outline, used only inside "Remove this account".
- Inputs are 36px, hairline border, neutral 3px focus ring.

## Constraints

- CSP is `style-src 'self'; font-src 'self'`: no inline `style` attributes. Data-driven geometry (the distribution bar) uses SVG attributes.
- Tests assert on `<dt>…</dt><dd>…</dd>` pairs and section `aria-label`s; keep those exact when restyling.
