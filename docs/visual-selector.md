# Visual selection

Choose **New feed**, enter a source URL, then **Choose elements visually**. A feed name and selectors are not needed to load the page. The workspace keeps CSS/XPath inputs on the left and the page preview on the right; on narrow screens they stack vertically.

1. Click a headline or repeating article card. The picker suggests its containing repeated card. Use **Selection level** to choose a larger or smaller container, or **Select parent** to move up one level.
2. Choose a suggestion and check its match count. Matching cards are outlined immediately; changing the suggestion updates those outlines. **Use selection** sets the repeated-item selector.
3. Select **Title** from the field menu and click a title inside any matched card. Apply the suggestion. Repeat for Link, Description, Image, and Date as needed.
4. Edit selectors directly in the left pane to tune their matches. Focus a field to highlight its first matching element across the repeated items. Changes update the same loaded snapshot without fetching the site again. Invalid expressions show an error alongside the preview.
5. To refine a CSS recipe in XPath, choose **Convert to XPath**, then edit the generated expressions. Visual picking also works while XPath is selected. For example, a title can use `.//h2` and a link can use `.//a/@href`.
6. Choose **Done** to return to the feed form with your edits intact, then **Preview items**. This fetches the source again and runs the standard extractor. Check the results before saving.

Each field under **Fine-tune selectors** has a **?** button with its purpose, blank/default behavior, and examples for the selected CSS or XPath syntax. Open a tip by click, tap, or keyboard; dismiss it with its close button, the same **?**, or Escape.

**Selector type** changes how the existing text is interpreted; it does not rewrite or clear expressions. **Convert to XPath** explicitly translates common CSS forms used by the picker, including tags, classes, IDs, attribute tests, descendant/child paths, and supported positional or `:has()` suggestions. Unsupported CSS reports an error and preserves the original recipe. Conversion is available from CSS to XPath; other expressions can be edited manually.

Applying a different repeated-item suggestion clears field selectors so paths for a previous card do not carry over. Reapplying the same suggestion keeps them. Typing a repeated-item selector also keeps the fields, making it possible to tune the card expression without rebuilding the recipe. Field suggestions are relative to each card; `.` means the card itself. XPath fields must start with `.` and are evaluated against isolated copies of the cards. Link selection moves to a containing anchor and suggests `href`; dates suggest `datetime` when present. Image fields retain the extractor's lazy-image detection.

The match badge counts repeated items or cards with a matching field. Attribute and text-node XPath results highlight their containing element. An empty field selector has no explicit highlight, even when the extractor supplies a default such as automatic image detection.

**Page version** controls the snapshot: Static HTML uses the HTTP fetcher; Rendered with Chromium uses the managed browser pool and the recipe's readiness/wait settings; FlareSolverr (Cloudflare) uses the configured external service with its own timeout. Local Chromium wait settings do not apply to FlareSolverr. Unconfigured browser options are disabled. Use **Reload page** to fetch a fresh snapshot or load a different page version. Editing, applying selectors, or choosing **Done** uses the successfully loaded version as the feed's fetch mode. An existing Auto setting is preserved unless you explicitly change the page version. Auto recipes initially show static HTML because a new recipe has no extraction selectors to decide whether fallback is needed. See [FlareSolverr setup](flaresolverr.md) for challenge support and network limits.

## Manual selectors and images

Selectors can also be entered directly. For a typical article card:

| Field | CSS | XPath | Attribute |
| --- | --- | --- | --- |
| Repeated items | `article.card` | `//article` | — |
| Title | `h2` | `.//h2` | blank for text |
| Link | `a` | `.//a[1]` | `href` by default |
| Description | `.summary` | `.//div[@class='summary']` | blank for sanitized HTML |
| Image | `img` | `.//img` | blank for automatic detection |
| Date | `time` | `.//time` | blank for exact `datetime`, then text |

Use `.` for the card itself. Enter CSS attributes in the separate attribute field, rather than writing `a@href`. XPath may select attributes directly, such as `.//a/@href`. A configured link selector must return a safe URL or that card is skipped. Leaving the link selector empty uses a title-based identity, so a later title change creates a new item.

A preview can match cards but reject every item. Its diagnostics show whether each match has an empty title or a missing/unsafe link. Keep the Title attribute blank to read text. If the repeated item is an `<a>` element, use `.` for Link with attribute `href`; if it contains the anchor, use `a` in CSS or `.//a` in XPath. A link outside the selected item requires choosing a larger repeated container. Missing dates, descriptions, or images do not cause items to be skipped.

Warnings distinguish a selector matching nothing from a selected element with an empty value or missing attribute. For an unexpected value, they show what the field expected and a short sample of what it received, such as `Date expected a date or relative age but received "First story"`. A newly published post may temporarily have no image; this is an optional-field warning and the next refresh can fill it in. Open **Preview diagnostics** for HTTP status and per-attempt match counts, or the saved feed's **Diagnostics** for [refresh history](operations.md#feed-diagnostics).

Automatic image detection tries `data-src`, `data-lazy-src`, usable `src`, then the last usable candidate in `data-srcset` or `srcset`. It does not evaluate viewport widths. An explicit attribute overrides detection; an empty image selector uses the card's first descendant image. Links and images become absolute using the effective page URL and a valid `<base>` URL. Extracted content is sanitized.

The item preview and RSS/Atom readers load images directly from their source. Hotlink protection, expired URLs, and HTTPS mixed-content restrictions can prevent display. See [publication dates](dates.md) for exact timestamps, relative dates, and custom formats.

## Preview boundary

The server parses a bounded fetched/rendered page and sends JSON containing text and selector metadata. It removes script and stylesheet content, event handlers, `style`/`srcdoc` attributes, script URL values, and active/embed subtrees. Useful attributes such as IDs, classes, `data-*`, `aria-*`, `href`, image URLs, and dates are retained only as plain values in an inert XML document used for matching. Text nodes preserve whitespace, and hidden subtree roots retain their original sibling positions without source attributes or children.

The visible preview is built separately using DOM creation and `textContent`. Source attributes, URLs, styles, and scripts are never applied to its HTML elements, and source resources are not loaded by the selection view. Images appear as labeled placeholders, so the layout differs from the original page. The extracted-item preview still shows normalized source images as before.

The selection surface is a local iframe with `sandbox="allow-scripts"`, without `allow-same-origin`. Its separate response CSP has `default-src 'none'`, permits only the app's nonce-authorized bridge and stylesheet, prohibits forms/base URLs, restricts ancestors to the app origin, and repeats the sandbox restriction. The shell is public but contains no source or session data. The authenticated parent sends the snapshot after checking the exact iframe WindowProxy and its opaque `null` origin. Subsequent messages must also match a fresh 128-bit channel token and bounded message schema. The bridge only accepts messages from its parent at the browser-facing app origin. No arbitrary script evaluation or source navigation is supported.

These isolation choices follow the [HTML iframe sandbox specification](https://html.spec.whatwg.org/multipage/iframe-embed-object.html#attr-iframe-sandbox) and [cross-document messaging specification](https://html.spec.whatwg.org/multipage/web-messaging.html#posting-messages).

Snapshots require a session, Origin validation, JSON, and CSRF. Fetching shares refresh slots and the selected fetcher's deadline and concurrency pool. Static and local Chromium requests use the existing outbound policy; FlareSolverr checks initial/final source URLs locally and depends on the external service's network boundary for remote browser traffic. Requests are cancelled when the picker is closed or reloaded, although a remote browser may continue until its own timeout. Source and encoded snapshot limits are 4 MiB; preview trees are limited to 12,000 nodes and 100 levels. Each element retains at most 64 allowed attributes with XML-safe names of at most 128 bytes and values of at most 2,048 bytes. Oversized pages return an error and can use manual selectors. Snapshots live only in the current picker, with no server cache or database changes.

## Limits

Suggestions are a starting point: repeated classes may include unrelated cards, and positional selectors can change when a site adds elements. Match counts describe the snapshot; a fresh page can differ. Live XPath matching uses the browser's [standard XPath evaluation](https://developer.mozilla.org/en-US/docs/Web/API/Document/evaluate). **Preview items** verifies the recipe with the Go extractor used by scheduled refreshes. Differences between the browser and Go evaluators, omitted attributes/subtrees, and snapshot limits can affect matches, so use that final preview before saving.

Repeated-item selectors must return elements, and XPath field selectors must return nodes rather than scalar values such as `count(...)`. Live matching limits expressions to 1,000 characters and repeated items to 1,000 matches; broadly overlapping cards can also exceed the workspace's isolated-copy limit. Content inside discarded embeds, SVG, shadow roots, and interactive controls cannot be selected. Unusual CSS class/ID syntax is omitted from suggestions in favor of element/positional paths. The simplified selection view omits source styles and image downloads.

The [example recipes](examples/all-visual-recipes.json) provide starting points for several public sites. Preview them before use because source layouts can change.
