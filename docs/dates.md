# Publication dates

Select a date element with the **Date** selector. Machine dates such as `2026-09-07T14:28:00Z` or `Thu, 5 Nov 2026 10:00 EST`, English dates such as `5 November 2026` or `Nov 5, 2026 at 3:04 pm`, and English relative phrases such as `2 minutes ago` are recognized automatically. No additional setting is needed.

## Choosing the date value

When **Date attribute** is blank and the selector matches an element, the app first looks for a valid standard date in that element’s `datetime` attribute. This preserves the precise timestamp in markup such as:

```html
<time datetime="2026-09-07T14:28:00Z">2 minutes ago</time>
```

If no valid standard `datetime` is available, the app parses the selected element’s text instead. It does not search elsewhere in the card for a date.

An explicit attribute name, such as `title`, reads only that attribute. XPath attribute and text selections also keep their meaning: `.//time/@datetime` reads the selected attribute, while `.//time/text()` reads the selected text without automatically substituting another value.

For explicitly selected values or element text, the app tries, in order:

1. the optional **Date settings → Go date layout**;
2. the [standard formats](#standard-formats);
3. [English dates with a month name](#month-names);
4. [relative phrases](#relative-phrases).

Automatically detected `datetime` attributes use only the standard formats, so `datetime="5 November 2026"` is skipped and the element’s text is read instead. Missing or unrecognized dates fall back to the time the story is first saved, and the preview warns `date not recognized`.

## Standard formats

- ISO 8601 and RFC 3339 dates, with a `T` or a space, with or without seconds or a fraction, and with an offset, `Z` or no zone: `2026-09-07T14:28:00Z`, `2026-09-07 14:28:00+02:00`, `2026-09-07T14:28`, `2026-09-07`.
- RFC 1123 and RFC 5322 dates, as HTTP headers and RSS feeds write them, with a numeric offset or a [zone name](#time-zones): `Mon, 07 Sep 2026 14:28:00 -0400`, `Mon, 7 Sep 2026 14:28 EDT`. The day may have one or two digits and the seconds may be left out.

## Month names

English dates with a month name are recognized in element text and explicitly selected values:

| Text | Date |
| --- | --- |
| `5 November 2026`, `5th Nov. 2026` | Local midnight on 5 November 2026 |
| `November 5, 2026`, `Nov 5 2026`, `Sept. 22nd, 2026` | Local midnight on that date |
| `Thursday, 5 November 2026 at 3:04 pm`, `Thu, Nov 5, 2026, 15:04` | 15:04 local time |
| `5 Nov 2026 10:00 EST`, `Nov 5, 2026 10:00:30 AM +01:00` | That time in the stated [zone](#time-zones) |

Months may be full names, three-letter abbreviations with or without a dot, or `Sept`; letter case does not matter. A weekday is optional but must match the date. A time may follow the date after a space, a comma or `at`, as `15:04`, `15:04:05`, `3:04 pm` or `3:04 p.m.`, and may end with a zone name or a numeric offset. A date without a time is local midnight in the recipe’s time zone, as `2026-11-05` is.

The whole value must be the date. `Posted on 5 November 2026` and `Nov 5, 2026 by Staff` are not recognized: narrow the selector or set a Go date layout. A year is required and must have four digits, so `Nov 5` and `Nov 5, 26` are not recognized. Other languages are not recognized either.

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

Calendar subtraction clamps dates to the last valid day of the destination month: March 31 minus one month becomes February 28 (or February 29 in a leap year).

## Time zones

Use **Date settings → Time zone** for the source site’s IANA time zone, such as `America/New_York`. Blank uses UTC. This applies to dates without an explicit offset, local midnight, calendar month/year calculations, and zone names. All resulting timestamps are stored in UTC.

A numeric offset such as `-0500` or `+01:00` is used as written, and `UTC` is always UTC. A zone name such as `EST` is read in this order:

1. **The recipe’s time zone.** If that zone uses the name, its meaning there wins: `CST` is China Standard Time (+08:00) under `Asia/Shanghai`, and `PST` is Philippine Standard Time (+08:00) under `Asia/Manila`. A name written out of season, such as `EDT` in December under `America/New_York`, keeps the offset the zone gives it.
2. **RFC 5322.** Otherwise `UT` and `GMT` are UTC, `EST` and `EDT` are −05:00 and −04:00, `CST` and `CDT` −06:00 and −05:00, `MST` and `MDT` −07:00 and −06:00, and `PST` and `PDT` −08:00 and −07:00. `GMT+3`, `GMT-5` and `GMT+0` are read as those offsets.
3. **Anything else is not guessed.** Abbreviations are not unique: `IST` is used in India, Ireland and Israel. A name such as `CET` or `IST`, or an hour such as `+03` written in place of a name, therefore makes the date unrecognized when the recipe’s time zone does not use it, and the story falls back to first-seen time with a warning rather than a time that may be hours wrong. Set the site’s time zone to have it read: `CET` under `Europe/Paris`, or `IST` under `Asia/Kolkata`.

The same rules apply to a Go date layout that contains the zone name element `MST`.

## Preview and saved dates

**Preview items** labels relative results **Estimated date**, shows the calculated date, and quotes the source phrase. Preview dates use the current fetch time and are displayed in your browser’s local time zone. Saving an enabled feed triggers its own fetch, so its initial estimate may differ slightly from the preview.

Once a story is saved, its publication date stays fixed on later refreshes, even if the website changes from `2 minutes ago` to `3 minutes ago`. This applies to exact dates, estimates, and first-seen fallbacks. Changing the date settings, or upgrading to a version that recognizes more dates, leaves stored publication dates unchanged, including dates that fell back to first-seen time; only stories first saved afterwards use the new reading. RSS and Atom use the same saved publication date.
