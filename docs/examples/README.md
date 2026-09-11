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
| [all-demo-recipes.json](all-demo-recipes.json) | All three, to import in one step |

## Run the demo

Serve the demo site, then start RSS Workshop with loopback fetching allowed:

```sh
cd docs/examples/demo-site
python3 -m http.server 8000 --bind 127.0.0.1
```

The recipes fetch `http://127.0.0.1:8000/index.html`. RSS Workshop blocks
private and loopback addresses by default, so allow that one address:
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

Recipes are portable: see [recipe import and export](../recipe-portability.md).
To build one against a real site, use the [visual selector](../visual-selector.md).
