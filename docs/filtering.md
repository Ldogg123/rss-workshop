# Story filters

Use **Story filters** in the feed editor to save stories that match your interests. Filtering runs after CSS/XPath extraction, so the selectors still choose the repeating cards and their fields.

## Include and exclude rules

An item is included when it passes the include rules and does not pass the exclude rules. Leave include rules empty to accept everything except your exclusions; leave both empty to keep every valid story.

Each condition selects **Title**, **Description**, or **Link** and contains a list of keywords or phrases, one per line:

- **Contains any** passes when at least one phrase occurs in that field.
- **Contains all** passes when every phrase occurs in that field.
- **All** groups require every child condition or group to pass.
- **Any** groups require at least one child to pass.

Groups can be nested. For example, include a story when its title contains either `Linux` or `self hosting`, **and** its description contains `Docker`. Exclude it if its link contains `/sponsored/` or `/advertisement/`.

Matching is case-insensitive and uses literal substrings: `AI` also matches the letters in `daily`. Phrases stay together; `home server` is one phrase. Whitespace is normalized, and Unicode case variants use simple case folding. There are no regular expressions, wildcard operators, stemming, or automatic accent removal. Description rules examine text from the extracted description, without HTML tags, image URLs, scripts, or styles. Link rules examine the resolved item URL. A missing description or link behaves as empty text.

You can paste 100 keywords into a single condition. Each feed supports up to **500 keywords**, **128 conditions/groups**, and **8 nested levels**; each keyword can contain up to **256 Unicode characters**. The full recipe must still fit the existing **64 KiB** limit. Invalid or incomplete rules are rejected before saving or fetching the source.

## Library filters

Rules you want on several feeds, such as dropping sponsored posts, can live in the **Filter library** instead of being copied into each feed. Choose **Filter library** in the library header, create a filter with a name and the same include and exclude panels, then select it under **Library filters** in any feed's **Story filters**.

Feeds use library filters by reference. Editing one in the library changes every feed using it, starting with each feed's next refresh; the dialog lists those feeds before you save. A story must pass the feed's own rules and every library filter it uses: all include sides must match, and no exclude side may match.

The limits above apply to each feed's combined rules, so a feed with its own rules and several library filters still has at most **500 keywords**, **128 conditions/groups**, and **8 nested levels** in total. A feed can use up to **16** library filters, and the library holds up to **200**. Saving a feed or a library filter that would push any feed past a limit is rejected; when a library filter edit is the cause, the message names the affected feed.

Editing a library filter works like editing a feed's rules. Saved stories are kept by default. Select **Remove already-saved stories that no longer match** to apply the new rules to the saved history of every feed using the filter, in one transaction with the edit and within the same five-second deadline. Refreshes already in progress when you save are discarded, and each affected feed fetches a full copy of its page next time rather than reusing an unchanged response.

A library filter that feeds still use cannot be deleted. Remove it from those feeds first; the library shows which feeds use each filter.

## Preview before saving

**Preview items** distinguishes matched card elements, valid extracted stories, included stories, and filtered stories. It shows the included items and up to 20 excluded-title examples with reasons. The counters cover the complete result even when there are more examples.

If all valid stories fail your filters, the preview succeeds with zero included items. Auto mode does not retry with Chromium just because your filters excluded everything. Fetch failures and missing required title/link fields remain extraction errors, with the normal [diagnostics](operations.md#feed-diagnostics).

Previews do not save stories, change filters on an existing feed, or remove saved history. After saving, refresh diagnostics also record the valid, included, and filtered counts.

## Previously saved stories

By default, saving filters preserves your existing library. Future refreshes save or update only matching stories; an older saved copy remains unchanged if the newly fetched version no longer matches. Stories already retained before adding filters can therefore still appear in the RSS/Atom output.

When editing a feed, select the option to **remove already-saved stories that do not match** if you want to apply the new rules to saved history too. Saving evaluates the new filters against the currently stored title, description, and link, and removes nonmatching entries in the same transaction as the recipe change. Matching stories keep their identity and publication date. The checkbox is a one-time action and resets when the editor opens again.

Applying rules to saved history has a five-second deadline. If a large history and complex description rules take too long, the transaction rolls back; try simpler rules or leave the history option unchecked.

Removing a filter does not restore deleted or previously excluded stories automatically. They can be collected on a later refresh only if the source still lists them. A [database backup](operations.md#backup-and-restore) preserves older history separately.

The normal item-count retention still applies. A fetch failure or extraction that finds no valid stories preserves previously saved output. Reading an RSS/Atom feed or its diagnostic history never fetches the source.

## Recipe files and API

Filters travel with [recipe exports and imports](recipe-portability.md). They are optional `recipe.filters` settings in the existing version-1 document format. Older servers reject filter-bearing recipe files rather than silently importing them without rules.

A recipe file cannot refer to another server's library, so an exported recipe carries each feed's own rules combined with its library filters. Importing it creates a feed with those rules as its own, not linked to any library filter.

```json
{
  "include": {
    "op": "all",
    "rules": [
      {"op": "contains_any", "field": "title", "keywords": ["Linux", "self hosting"]},
      {"op": "contains_all", "field": "description", "keywords": ["Docker"]}
    ]
  },
  "exclude": {
    "op": "contains_any",
    "field": "link",
    "keywords": ["/sponsored/", "/advertisement/"]
  }
}
```

Groups use `op: "all"` or `"any"` with a nonempty `rules` array. Conditions use `"contains_any"` or `"contains_all"`, one `field` (`"title"`, `"description"`, or `"link"`), and a nonempty `keywords` array. Do not combine group and condition fields on the same node. Empty include/exclude sides are omitted.

The authenticated feed-save API accepts a top-level `apply_filters_to_history: true` for the optional history cleanup. It defaults to false and is not part of the recipe or export. The usual Origin and CSRF protections apply. Database schema 3 prevents older app versions from reopening the database and ignoring its filters, and schema 5 does the same for library filters; follow the [upgrade guidance](operations.md#upgrades) before switching versions.

Feed saves and previews also accept a top-level `filter_ids` array naming library filters in order. Omitting it keeps a saved feed's current library filters, so older clients and pause/resume requests do not unlink them; an empty array removes them all. Feed listings return `filter_ids` for every feed.

| Route | Behavior |
| --- | --- |
| `GET /api/filters` | List library filters with `id`, `name`, `filters`, and the `feed_ids` using each. |
| `POST /api/filters` | Create a filter from `name` (1–100 characters) and `filters`; at least one side is required. |
| `PUT /api/filters/{id}` | Replace a filter's name and rules, applying them to every feed using it. Accepts `apply_filters_to_history`. |
| `DELETE /api/filters/{id}` | Delete an unused filter. Returns HTTP 409 while feeds use it. |

Rejected rules and combinations return HTTP 400 with a message describing the problem.
