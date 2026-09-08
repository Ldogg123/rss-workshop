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

The authenticated feed-save API accepts a top-level `apply_filters_to_history: true` for the optional history cleanup. It defaults to false and is not part of the recipe or export. The usual Origin and CSRF protections apply. Database schema 3 prevents older app versions from reopening the database and ignoring its filters; follow the [upgrade guidance](operations.md#upgrades) before switching versions.
