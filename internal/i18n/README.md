# internal/i18n

Self-contained localization for rave-mate. All translations ship in the binary
(`locales/*.json`, `go:embed`ed) - no network, no external service, no new dependency.

## Model

- `en.json` is the **source of truth**. Every user-facing string lives here first.
- Other locales (`de.json`, …) translate a subset; anything missing **falls back to en**,
  then to the raw key. A user never sees an empty string or a bare key when en has the text.
- Keys are **nested JSON**, flattened to dotted keys internally: `settings.section.account.title`.
- The `rave·mate` brand wordmark is **never** translated - it is literal in the renderer.

## Using keys in code

```go
import "rave.page/mate/internal/i18n"

i18n.T("tab.settings")                              // "Einstellungen" (de) / "Settings" (en)
i18n.T("placeholder.comingSoon", i18n.A{"name": x}) // {name} interpolation
i18n.Tn("track", n)                                 // plural: n==1 → track.one, else track.other; injects {count}
```

- `T(key, data...)` - translate; optional `A{}` map fills `{name}` placeholders.
- `Tn(key, n, data...)` - plural. Looks up `key.one` (n==1) or `key.other`, auto-injects `{count}`.
- Prefer named `{placeholders}` over string concatenation so translators can reorder words.

## Locale resolution & persistence

Order (in `SetLocale`): **explicit user setting** → **OS locale** → **en**.

- Persisted in config under `features.ui.language` (`config.UIFeature.Language`).
- `app.go` calls `i18n.SetLocale(cfg.Features.UI.Language)` at startup.
- The Settings tab language switcher writes the field, calls `SetLocale`, saves config, re-renders.
- OS locale: POSIX `LC_ALL`/`LC_MESSAGES`/`LANG`/`LANGUAGE` env vars; on Windows
  `GetUserDefaultLocaleName` (stdlib syscall). Tags are normalized to their base subtag
  (`de-DE` → `de`).

## Adding a language

1. Copy `locales/en.json` to `locales/<code>.json` (e.g. `fr.json`). Keep the nesting.
2. Set `_meta.name` to the language's own name (`"Français"`) - it labels the switcher option.
3. Translate the values. Leave any key untranslated to fall back to English; delete keys you
   haven't done - **do not** leave English placeholders (they'd hide what's missing).
4. `go test ./internal/i18n/` - `TestTranslationCompleteness` prints coverage + the exact
   untranslated keys. New file is picked up automatically (embedded + shown in the switcher).

## Adding / changing a key

1. Add it to `en.json` (source of truth) in the right nested group.
2. Use it via `i18n.T("group.key")`.
3. Translate in other locales as desired (optional - falls back).

## Completeness guard (the CI-extraction equivalent)

`go test ./internal/i18n/`:

- **`TestLocalesWellFormed`** - FAILS if a locale is malformed, has a non-string leaf, if
  `en.json` is empty, or if a non-en locale has **stale keys** (present in the locale but not
  in en - a typo or a removed string). These are real bugs.
- **`TestTranslationCompleteness`** - logs per-locale coverage % and lists untranslated keys.
  Informational by default (keeps `go test ./...` green with a partial locale). Set
  `I18N_STRICT=1` to make untranslated keys a hard failure - use for a full-coverage CI lane.

## Converting more of the UI

The webui renders HTML in Go (`internal/webui/render_*.go`). To localize a hardcoded string:

1. Add the string to `en.json`.
2. Replace the Go literal with `i18n.T("…")` (import `rave.page/mate/internal/i18n`).
3. Batches already converted: main tab labels + nav tooltips (`render.go`), the Settings
   header + section titles/descriptions + language card (`render_settings.go`), and the
   player/publish transport labels (`player.go`, `render_publish.go`). Follow the same pattern
   for the remaining tabs. Convert per-tab and verify with `rave-mate ctl screenshot-all`.
