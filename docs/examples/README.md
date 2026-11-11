# Example recipes

These recipes build feeds from **the demo site in this folder**, a fictional
local newspaper. Nothing here scrapes a real publisher, so the examples cannot
break when somebody else redesigns their pages, and there is no question about
whose terms you are agreeing to while you learn the tool.

| File | Shows |
| --- | --- |
| [demo-basic-recipe.json](demo-basic-recipe.json) | Every field: title, link, date from a `datetime` attribute, description, and image |
| [demo-xpath-recipe.json](demo-xpath-recipe.json) | The same feed written in XPath instead of CSS |
| [demo-full-content-recipe.json](demo-full-content-recipe.json) | Story filters excluding a sponsored card, plus full article content from each story's own page |
| [demo-feed-recipe.json](demo-feed-recipe.json) | A [feed source](../feed-sources.md): the site's own RSS feed, with the same filter and full article content, and no selectors |
| [all-demo-recipes.json](all-demo-recipes.json) | All four, to import in one step |

## Try it without serving anything

The documentation site publishes this demo site at
[ldogg123.github.io/rss-workshop/examples/demo-site/](https://ldogg123.github.io/rss-workshop/examples/demo-site/index.html),
a public address that RSS Workshop fetches without an `ALLOW_CIDRS` exception.
Enter it as the page of a **New feed** to try the
[visual selector](../visual-selector.md) on it, or save and import
[all-demo-recipes.json for the published demo](https://ldogg123.github.io/rss-workshop/examples/hosted/all-demo-recipes.json):
the four recipes above with that address in place of `http://127.0.0.1:8000/`,
written when the site is built. The published demo follows the `main` branch.

The recipes in this folder keep the local address on purpose. They work
offline, the tests import them and extract from the demo site beside them, and
a feed built from them does not depend on the published copy.

## Run the demo

Serve the demo site, then start RSS Workshop with loopback fetching allowed:

```sh
cd docs/examples/demo-site
python3 -m http.server 8000 --bind 127.0.0.1
```

The recipes fetch `http://127.0.0.1:8000/index.html`, and the feed source
`http://127.0.0.1:8000/feed.xml`. RSS Workshop blocks private and loopback
addresses by default, so allow that one address:
`ALLOW_CIDRS=127.0.0.1/32` in `.env`, or exported before a native run. Leave it
unset again afterwards — see [deployment](../deployment.md).

Then choose **Import recipes**, select `all-demo-recipes.json`, and preview.
Imported feeds arrive **paused** with fresh reader links, so resume one before
refreshing it.

## What to look at

The demo front page is built the way real news sites are built, so the
selectors are the ones you would write for a real source:

- `.story` is the repeating card. The page also has a lead story above the list
  that is deliberately not a `.story`, which is why the repeating-element
  selector matters.
- The date lives in `<time datetime="...">`. The examples read the `datetime`
  attribute rather than the human text, which is what keeps dates exact.
- The image is an `<img src>` inside the card.
- One card is a sponsored placement. `demo-full-content-recipe.json` excludes it
  by title, which is the shape most real filters take.
- Each story links to its own page, where `.article-body` holds the full text.
  The front page carries only a one-line standfirst, so full article content is
  the difference between a teaser and something worth reading.
- `feed.xml` is the site's own RSS feed, listing the same five stories with
  their dates and images. `demo-feed-recipe.json` reads it as a feed source, so
  it needs no selectors, then applies the same filter and article selector.

Recipes are portable: see [recipe import and export](../recipe-portability.md).
To build one against a real site, use the [visual selector](../visual-selector.md).
