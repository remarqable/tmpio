# Content format

This document describes what tmp accepts as a page, how links and images resolve, the path grammar, the `tmp.yaml` schema, and how the sidebar orders entries. The implementation is `internal/platform/render` (pages), `internal/models/paths.go` (paths) and `internal/models/siteconfig.go` (config). The format version advertised by `tmp_info` is `tmp-format/1`.

## Files that are not pages

Besides Markdown pages, tmp stores plain text files an AI commonly produces: `.txt .log .csv .tsv .json .yaml .yml .toml .xml .ini` and code files `.py .js .ts .go .rs .rb .sh .sql .css`. They are written through the same `tmp_write`, REST and editor paths, versioned, searchable and exported like pages, but never interpreted as markup. JSON and YAML must parse. Browsers get a read-only viewer (CSV and TSV as a table, everything else as a code block); agents, `curl` and `?raw=1` get the raw bytes with the file's MIME type. Files have no JSON representation URL and cannot be shared with a secret link. Binary assets are PNG, JPEG, WebP (5 MiB, 20 megapixels) and PDF (20 MiB by default, shown inline by the browser). HTML, CSS and JavaScript files are refused.

## Pages

A page is a `.md` file: optional YAML frontmatter, then Markdown. Maximum size is `QUOTA_MAX_PAGE_BYTES` (default 256 KiB). Content must be valid UTF-8 without NUL bytes. A missing trailing newline is added on save.

```markdown
---
title: Circle
description: Research on Circle and its community platform
tags: [community, competitor]
order: 20
---

# Circle

## Files that are not pages

Besides Markdown pages, tmp stores plain text files an AI commonly produces: `.txt .log .csv .tsv .json .yaml .yml .toml .xml .ini` and code files `.py .js .ts .go .rs .rb .sh .sql .css`. They are written through the same `tmp_write`, REST and editor paths, versioned, searchable and exported like pages, but never interpreted as markup. JSON and YAML must parse. Browsers get a read-only viewer (CSV and TSV as a table, everything else as a code block); agents, `curl` and `?raw=1` get the raw bytes with the file's MIME type. Files have no JSON representation URL and cannot be shared with a secret link. Binary assets are PNG, JPEG, WebP (5 MiB, 20 megapixels) and PDF (20 MiB by default, shown inline by the browser). HTML, CSS and JavaScript files are refused.

## Overview

Research text with [sources](https://example.com).

:::callout type="note"
This assessment is a working draft.
:::
```

Supported Markdown: headings, paragraphs, emphasis, strikethrough, blockquotes, ordered and unordered lists, task lists, fenced code, tables (with cell alignment), links, autolinks, images, thematic breaks. Headings get stable `id` attributes: lowercase ASCII letters, digits and single hyphens; duplicates get `-2`, `-3`, and so on; an empty slug becomes `section`. H2 and H3 headings form the table of contents.

One primary title is shown. If the first block is an H1 whose text equals the resolved title, it is removed from the body so the title is not printed twice.

### Files that are not pages

Besides Markdown pages, tmp stores plain text files an AI commonly produces: `.txt .log .csv .tsv .json .yaml .yml .toml .xml .ini` and code files `.py .js .ts .go .rs .rb .sh .sql .css`. They are written through the same `tmp_write`, REST and editor paths, versioned, searchable and exported like pages, but never interpreted as markup. JSON and YAML must parse. Browsers get a read-only viewer (CSV and TSV as a table, everything else as a code block); agents, `curl` and `?raw=1` get the raw bytes with the file's MIME type. Files have no JSON representation URL and cannot be shared with a secret link. Binary assets are PNG, JPEG, WebP (5 MiB, 20 megapixels) and PDF (20 MiB by default, shown inline by the browser). HTML, CSS and JavaScript files are refused.

## Frontmatter

The block starts with a line that is exactly `---` on line 1 and ends at the next `---` line. It must be a mapping. Limits:

| Key | Type | Limit | Notes |
|---|---|---|---|
| `title` | string | 200 characters | Falls back to the first H1, then to the file name without `.md`. |
| `description` | string | 500 characters | Shown in JSON, listings and cards. |
| `tags` | list of strings | 20 tags, 64 characters each | |
| `order` | integer | | Sidebar sort key. Must be a YAML integer. |
| `extra` | mapping | | Free-form data kept with the revision. Not rendered. |

Any other top-level key is rejected: `unknown frontmatter key "x"; custom fields belong under extra:`. Whole-block limits: 16 KiB, nesting depth 8, no anchors, no aliases, no duplicate keys. An unterminated block is an error. Errors carry the 1-based line number of the original source.

Server timestamps and revision numbers are authoritative. Frontmatter cannot set them.

### Files that are not pages

Besides Markdown pages, tmp stores plain text files an AI commonly produces: `.txt .log .csv .tsv .json .yaml .yml .toml .xml .ini` and code files `.py .js .ts .go .rs .rb .sh .sql .css`. They are written through the same `tmp_write`, REST and editor paths, versioned, searchable and exported like pages, but never interpreted as markup. JSON and YAML must parse. Browsers get a read-only viewer (CSV and TSV as a table, everything else as a code block); agents, `curl` and `?raw=1` get the raw bytes with the file's MIME type. Files have no JSON representation URL and cannot be shared with a secret link. Binary assets are PNG, JPEG, WebP (5 MiB, 20 megapixels) and PDF (20 MiB by default, shown inline by the browser). HTML, CSS and JavaScript files are refused.

## Extensions

Exactly three container extensions exist. An extension opens with `:::name` optionally followed by `key="value"` attributes (double quotes only) and closes with a line that is exactly `:::`. The opening fence must be indented fewer than 4 spaces. Content inside is ordinary Markdown. Fenced code blocks inside an extension are opaque: a `:::` line within them does not close the block.

| Extension | Syntax | Rendered HTML |
|---|---|---|
| Callout | `:::callout type="note"` ... `:::` with `type` one of `note`, `tip`, `warning`, `important` | `<div class="tmp-callout tmp-callout-note" role="note"><div class="tmp-callout-title">Note</div><div class="tmp-callout-body">...</div></div>` |
| Cards | `:::cards` wrapping one Markdown list; each item holds exactly one link and optional text after it | `<div class="tmp-cards"><a class="tmp-card" href="..."><span class="tmp-card-title">...</span><span class="tmp-card-desc">...</span></a>...</div>` (external hrefs get `rel="noopener noreferrer"`) |
| Details | `:::details title="More information"` ... `:::`; `title` is required | `<details class="tmp-details"><summary>More information</summary><div class="tmp-details-body">...</div></details>` |

Example cards block:

```markdown
:::cards
- [Circle](/research/circle) Community platform notes
- [Discord](https://discord.com) External reference
:::
```

Leading `-`, `:` or dash characters between the link and the description are trimmed.

### Files that are not pages

Besides Markdown pages, tmp stores plain text files an AI commonly produces: `.txt .log .csv .tsv .json .yaml .yml .toml .xml .ini` and code files `.py .js .ts .go .rs .rb .sh .sql .css`. They are written through the same `tmp_write`, REST and editor paths, versioned, searchable and exported like pages, but never interpreted as markup. JSON and YAML must parse. Browsers get a read-only viewer (CSV and TSV as a table, everything else as a code block); agents, `curl` and `?raw=1` get the raw bytes with the file's MIME type. Files have no JSON representation URL and cannot be shared with a secret link. Binary assets are PNG, JPEG, WebP (5 MiB, 20 megapixels) and PDF (20 MiB by default, shown inline by the browser). HTML, CSS and JavaScript files are refused.

## What is rejected

Validation fails with `422 validation_failed` on REST, `validation_failed` on MCP, and a line-numbered list in the editor. Nothing is saved.

| Input | Error |
|---|---|
| Raw HTML anywhere outside a code block (`<div>`, `<script>`, inline `<b>`) | `raw HTML is not allowed` |
| Unknown extension (`:::columns`) | `unknown extension "columns"` |
| Unknown attribute (`:::callout title="x"`) | `unknown attribute "title" for :::callout` |
| Bad attribute syntax (single quotes, missing quotes, duplicate attribute) | `invalid attribute syntax; expected key="value" pairs` |
| Unclosed block | `unclosed :::callout block` |
| A bare `:::` with nothing open | `closing ::: without an open extension block` |
| Extension inside an extension | `nested extension blocks are not allowed` (reported at the inner line) |
| Callout with another type | `callout type must be one of note, tip, warning, important` |
| Details without title | `details requires a title="..." attribute` |
| Cards item with zero or two links, or a non-list child | `each card must contain exactly one link (found N)`, `cards may only contain a single list of links` |
| Link with a scheme other than `https`, `http`, `mailto` (including `javascript:`, `data:`, with whitespace tricks) | `unsupported URL scheme "javascript"; only https:, http: and mailto: are allowed` |
| Link or image whose path climbs above the site root (`../../x`) | `link "..." escapes the site root` |
| Image with `http:` | `insecure image URL "..."; images must use https:` |
| Image with any other scheme | `unsupported image URL scheme` |
| Source larger than the limit | `input is N bytes; the maximum is M` |

After rendering, every page passes an allowlist sanitizer (bluemonday). It admits only the elements and attributes the renderer emits: the listed block and inline elements, `id` on headings, `href`/`title`/`rel` on `a`, `src`/`alt`/`title` on `img` (https or site-relative only), `class` values matching `tmp-*`, `language-*` and task-list classes, `align` on cells, checkbox inputs. Event handlers, `style`, `hx-*` attributes, forms, iframes and scripts cannot survive. Rendered content is never executed as a Go template.

### Files that are not pages

Besides Markdown pages, tmp stores plain text files an AI commonly produces: `.txt .log .csv .tsv .json .yaml .yml .toml .xml .ini` and code files `.py .js .ts .go .rs .rb .sh .sql .css`. They are written through the same `tmp_write`, REST and editor paths, versioned, searchable and exported like pages, but never interpreted as markup. JSON and YAML must parse. Browsers get a read-only viewer (CSV and TSV as a table, everything else as a code block); agents, `curl` and `?raw=1` get the raw bytes with the file's MIME type. Files have no JSON representation URL and cannot be shared with a secret link. Binary assets are PNG, JPEG, WebP (5 MiB, 20 megapixels) and PDF (20 MiB by default, shown inline by the browser). HTML, CSS and JavaScript files are refused.

## Links

| Form | Meaning |
|---|---|
| `/research/circle` or `/research/circle.md` | Site root. Rendered as `/research/circle` in the personal view and `/o:{org}/research/circle` in the explicit or shared view. |
| `circle.md`, `../about` | Relative to the directory of the source file. |
| `#overview` | Fragment on the current page; kept as is. |
| `/research/circle#overview` | Path plus fragment; fragment stays attached. |
| `https://...`, `http://...`, `mailto:...` | External. Rendered unchanged with `rel="noopener noreferrer"`. |

Rendered internal paths drop `.md`; `index.md` becomes its directory with a trailing slash. A query string on an internal link is removed with a warning. An internal link whose target does not exist (no live entry, no move alias) produces the warning `broken internal link: /path`; the page still saves. Warnings are returned in mutation results and shown in the editor.

Cross-organization links must be written as full explicit URLs; a site-relative link never resolves into another organization.

On a page move, relative links and images inside the moved page are rewritten so they keep pointing at the same targets. The move result lists each rewrite as `old -> new`.

### Files that are not pages

Besides Markdown pages, tmp stores plain text files an AI commonly produces: `.txt .log .csv .tsv .json .yaml .yml .toml .xml .ini` and code files `.py .js .ts .go .rs .rb .sh .sql .css`. They are written through the same `tmp_write`, REST and editor paths, versioned, searchable and exported like pages, but never interpreted as markup. JSON and YAML must parse. Browsers get a read-only viewer (CSV and TSV as a table, everything else as a code block); agents, `curl` and `?raw=1` get the raw bytes with the file's MIME type. Files have no JSON representation URL and cannot be shared with a secret link. Binary assets are PNG, JPEG, WebP (5 MiB, 20 megapixels) and PDF (20 MiB by default, shown inline by the browser). HTML, CSS and JavaScript files are refused.

## Images

- Local: `![Diagram](/assets/diagram.png)` or a relative path. The path is resolved against the source directory. If a live asset exists there, the `src` becomes its URL in the current address form. If not, the image is replaced by `<span class="tmp-asset-unavailable">Image unavailable: diagram.png</span>` with the warning `image unavailable: /assets/diagram.png`. This is a warning, not a rejection; upload the asset and the page renders it on the next request.
- External: `https://` only. tmp never fetches external URLs on the server.
- In shared views (`/s/{token}`), only owner-approved assets that are still embedded resolve; see SHARING.md.

Assets are PNG, JPEG or WebP, verified by file signature and decoded within `QUOTA_MAX_ASSET_PIXELS`. The extension must match the content (`.png` for PNG, `.jpg`/`.jpeg` for JPEG, `.webp` for WebP). Uploads are REST or browser only; MCP tools can list and reference assets but not upload them.

## Files that are not pages

Besides Markdown pages, tmp stores plain text files an AI commonly produces: `.txt .log .csv .tsv .json .yaml .yml .toml .xml .ini` and code files `.py .js .ts .go .rs .rb .sh .sql .css`. They are written through the same `tmp_write`, REST and editor paths, versioned, searchable and exported like pages, but never interpreted as markup. JSON and YAML must parse. Browsers get a read-only viewer (CSV and TSV as a table, everything else as a code block); agents, `curl` and `?raw=1` get the raw bytes with the file's MIME type. Files have no JSON representation URL and cannot be shared with a secret link. Binary assets are PNG, JPEG, WebP (5 MiB, 20 megapixels) and PDF (20 MiB by default, shown inline by the browser). HTML, CSS and JavaScript files are refused.

## Path grammar

Paths are site-relative and start with `/`. They are stored exactly as given; an `/o:{org}` qualifier in a URL is never part of the path.

| Rule | Detail |
|---|---|
| Characters | Segments use `a-z`, `0-9`, `-` and `_` only. Uppercase is rejected with `path must be lowercase; canonical form is "/..."` and the suggested path in `suggested_path`. |
| Segment length | at most 128 bytes |
| Whole path | at most 512 bytes and 16 segments |
| Pages | end in `.md` |
| Config | exactly `/tmp.yaml`; any other `.yaml` or `.yml` is rejected |
| Assets | end in `.png`, `.jpg`, `.jpeg` or `.webp` |
| Directories | a bare name (`/research`) or trailing slash (`/research/`). Write APIs treat a name without an extension as a directory; they never guess that `/research/circle` means a page. |
| Other extensions | rejected: `unsupported file extension ".txt" (use .md, .png, .jpg, .jpeg or .webp)` |
| Forbidden | empty segments (`//`), `.` and `..`, control characters, NUL, backslash, unpaired trailing slash on files |
| Reserved root segments | `s`, `app`, `auth`, `oauth`, `api`, `mcp`, `.well-known`, `static`, `healthz`, `readyz`, `metrics`, `login`, `logout`, `robots.txt`, `sitemap.xml`, `llms.txt`, `search`, and anything starting with `o:` (case-insensitive). Applies to the first segment of both the source path and the rendered path. `tmp.yaml` is reserved for the config entry. |

Request URLs are decoded once. Encoded `%2F`, `%5C`, `%00`, `%2E%2E` and `%25` sequences, control characters and Unicode format characters are rejected before decoding is attempted. Content URLs with uppercase letters return 404 rather than being normalized.

### Files that are not pages

Besides Markdown pages, tmp stores plain text files an AI commonly produces: `.txt .log .csv .tsv .json .yaml .yml .toml .xml .ini` and code files `.py .js .ts .go .rs .rb .sh .sql .css`. They are written through the same `tmp_write`, REST and editor paths, versioned, searchable and exported like pages, but never interpreted as markup. JSON and YAML must parse. Browsers get a read-only viewer (CSV and TSV as a table, everything else as a code block); agents, `curl` and `?raw=1` get the raw bytes with the file's MIME type. Files have no JSON representation URL and cannot be shared with a secret link. Binary assets are PNG, JPEG, WebP (5 MiB, 20 megapixels) and PDF (20 MiB by default, shown inline by the browser). HTML, CSS and JavaScript files are refused.

## Rendered namespace

Each entry has a rendered path: pages drop `.md`, `index.md` collapses onto its directory, directories and assets keep their path. The pair `(tenant, rendered_path)` is unique in the database, and the write path checks it again with the tenant lock held. Consequences:

- `/research.md` and the directory `/research/` cannot coexist: both render at `/research`.
- `/research/index.md` and the directory `/research` do coexist; the page is the directory's overview.
- A path left behind by a move (an alias) cannot be reused for a new entry: `... is a redirect left by a previous move and cannot be reused`.
- A deleted entry keeps its path in the trash. Writing to that path with `expected_revision` 0 revives it if the kind matches; a different kind is a `path_conflict`.

Directories are explicit records. Creating a page creates missing parents. `mkdir` on an existing directory succeeds without change. Deleting a non-empty directory fails. Root and `/tmp.yaml` cannot be deleted or moved.

## Files that are not pages

Besides Markdown pages, tmp stores plain text files an AI commonly produces: `.txt .log .csv .tsv .json .yaml .yml .toml .xml .ini` and code files `.py .js .ts .go .rs .rb .sh .sql .css`. They are written through the same `tmp_write`, REST and editor paths, versioned, searchable and exported like pages, but never interpreted as markup. JSON and YAML must parse. Browsers get a read-only viewer (CSV and TSV as a table, everything else as a code block); agents, `curl` and `?raw=1` get the raw bytes with the file's MIME type. Files have no JSON representation URL and cannot be shared with a secret link. Binary assets are PNG, JPEG, WebP (5 MiB, 20 megapixels) and PDF (20 MiB by default, shown inline by the browser). HTML, CSS and JavaScript files are refused.

## Site configuration: `/tmp.yaml`

The default written at signup:

```yaml
version: 1
theme: docs
appearance: system
navigation: []
sidebar:
  auto: true
features:
  search: true
  toc: true
```

Full schema:

| Key | Type | Allowed | Default when omitted | Notes |
|---|---|---|---|---|
| `version` | int | `1` | required | anything else fails |
| `name` | string | up to 120 chars | none | Display name. Fallback: organization name, then "Your site". |
| `description` | string | up to 500 chars | none | |
| `theme` | string | `docs`, `editorial` | `docs` | |
| `appearance` | string | `light`, `dark`, `system` | `system` | Readers can override locally in the browser. |
| `navigation` | list of `{title, path}` | at most 12 items | empty | `title` required, up to 80 chars. `path` must start with `/`, be site-local (no `://`, no `//`, no whitespace) and pass the request-path checks. Rendered with the current address prefix. |
| `sidebar.auto` | bool | | `true` | `true` builds the directory tree automatically; an explicit `false` hides the sidebar. |
| `features.search` | bool | | `true` | Shows the search form; an explicit `false` hides it. |
| `features.toc` | bool | | `true` | Shows the right-hand table of contents; an explicit `false` hides it. |

Booleans default to `true` only when the key is absent. Writing `sidebar: {auto: false}` or `features: {toc: false}` is honoured.

Validation is strict: unknown keys fail (`field x not found`), anchors and aliases fail, duplicate keys fail, nesting deeper than 8 fails, and the file is limited to `QUOTA_MAX_CONFIG_BYTES` (32 KiB). An invalid file is not saved and the previous configuration stays active. If the stored file ever fails to parse under a newer parser, the site falls back to defaults rather than failing to render.

The file controls presentation only. It has no keys for authentication, sharing, credentials, server paths, templates or scripts. Secret-link holders cannot read or write it.

## Files that are not pages

Besides Markdown pages, tmp stores plain text files an AI commonly produces: `.txt .log .csv .tsv .json .yaml .yml .toml .xml .ini` and code files `.py .js .ts .go .rs .rb .sh .sql .css`. They are written through the same `tmp_write`, REST and editor paths, versioned, searchable and exported like pages, but never interpreted as markup. JSON and YAML must parse. Browsers get a read-only viewer (CSV and TSV as a table, everything else as a code block); agents, `curl` and `?raw=1` get the raw bytes with the file's MIME type. Files have no JSON representation URL and cannot be shared with a secret link. Binary assets are PNG, JPEG, WebP (5 MiB, 20 megapixels) and PDF (20 MiB by default, shown inline by the browser). HTML, CSS and JavaScript files are refused.

## Sidebar ordering

With `sidebar.auto: true` the sidebar is built from every live entry:

1. Each directory becomes a node. If the directory has an `index.md`, that page supplies the node's title, link and `order`. The overview is therefore the directory entry itself and always leads its section; it is not listed again as a child.
2. Other pages become children of their directory node.
3. Children are sorted by `order` ascending (entries without `order` sort last), then by title (case-insensitive), then by path.
4. The same sorting applies recursively at every level.

Directory listings (a directory URL without an `index.md`) use the same nodes. Primary navigation (`navigation` in `tmp.yaml`) is independent of the sidebar.
