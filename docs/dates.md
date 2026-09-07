# Publication dates

Select a date element with the **Date** selector. Dates such as `2026-09-07T14:28:00Z` and English relative phrases such as `2 minutes ago` are recognized automatically. No additional setting is needed.

## Choosing the date value

When **Date attribute** is blank and the selector matches an element, the app first looks for a valid absolute date in that element’s `datetime` attribute. This preserves the precise timestamp in markup such as:

```html
<time datetime="2026-09-07T14:28:00Z">2 minutes ago</time>
```

If no valid absolute `datetime` is available, the app parses the selected element’s text instead. It does not search elsewhere in the card for a date.

An explicit attribute name, such as `title`, reads only that attribute. XPath attribute and text selections also keep their meaning: `.//time/@datetime` reads the selected attribute, while `.//time/text()` reads the selected text without automatically substituting another value.

For explicitly selected values or element text, an optional **Go date layout** is tried first, followed by standard absolute formats and then relative phrases. Automatically detected `datetime` attributes use standard absolute formats. Missing or unrecognized dates fall back to the time the story is first saved.

## Relative phrases

Supported English phrases include:

| Text | Estimated date |
| --- | --- |
| `now`, `just now` | Current fetch time |
| `2 minutes ago`, `an hour ago` | Fetch time minus the stated duration |
| `3 days ago`, `2 weeks ago` | Fetch time minus elapsed 24-hour days or seven-day weeks |
| `1 month ago`, `a year ago` | Same local date and time in an earlier calendar month or year |
| `today`, `yesterday` | Local midnight today or yesterday |

Seconds, minutes, hours, days, weeks, months, and years are supported, including common abbreviations and wording such as `about 2 hours ago`. Phrases are estimates: a site saying `1 month ago` may have rounded an age of several weeks. Other languages and unrecognized wording use first-seen time.

Use **Date settings → Time zone** for the source site’s IANA time zone, such as `America/New_York`. Blank uses UTC. This applies to absolute dates without an explicit offset, local midnight, and calendar month/year calculations. Calendar subtraction clamps dates to the last valid day of the destination month: March 31 minus one month becomes February 28 (or February 29 in a leap year). All resulting timestamps are stored in UTC.

## Preview and saved dates

**Preview items** labels relative results **Estimated date**, shows the calculated date, and quotes the source phrase. Preview dates use the current fetch time and are displayed in your browser’s local time zone. Saving an enabled feed triggers its own fetch, so its initial estimate may differ slightly from the preview.

Once a story is saved, its publication date stays fixed on later refreshes, even if the website changes from `2 minutes ago` to `3 minutes ago`. This applies to exact dates, estimates, and first-seen fallbacks. Existing stored publication dates are unchanged, including dates that previously fell back to first-seen time. RSS and Atom use the same saved publication date.
