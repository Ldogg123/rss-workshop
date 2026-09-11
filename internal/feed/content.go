package feed

import (
	"html"
	"strings"

	"rss-workshop/internal/model"
)

// itemContent is the full body both serializers publish for an item.
//
// An item's stored HTML is the list-page teaser, which already carries any
// extracted image inline. When an article body has been fetched it replaces
// that teaser, and the image is prepended the same way so a preview image the
// source published after the story went live still reaches the reader: the
// list page keeps refreshing image on every refresh, while the fetched article
// body is kept as it was retrieved.
//
// Article pages usually include the lead image inside the body they mark up, so
// prepending unconditionally showed it twice in every reader. Prepend only when
// the body does not already reference it.
func itemContent(it model.Item) string {
	if it.FullHTML == "" {
		return it.HTML
	}
	if it.Image == "" || strings.Contains(it.FullHTML, it.Image) {
		return it.FullHTML
	}
	return imageTag(it.Image) + it.FullHTML
}

// itemSummary is the short form for a reader's list view: the list-page teaser,
// but only when a fetched article body makes it genuinely shorter than the
// content. Without full article content the teaser IS the content, and
// repeating it as a summary would only duplicate bytes in every feed.
func itemSummary(it model.Item) string {
	if it.FullHTML == "" || it.HTML == "" {
		return ""
	}
	return it.HTML
}

func imageTag(src string) string {
	return `<p><img src="` + html.EscapeString(src) + `" alt=""></p>`
}

// imageType guesses a media type from the image URL so a reader knows what it
// is being offered. The image is never fetched to find out: reader requests
// must not reach the source, and neither must serialization.
func imageType(src string) string {
	if i := strings.IndexAny(src, "?#"); i >= 0 {
		src = src[:i]
	}
	switch {
	case strings.HasSuffix(src, ".png"):
		return "image/png"
	case strings.HasSuffix(src, ".gif"):
		return "image/gif"
	case strings.HasSuffix(src, ".webp"):
		return "image/webp"
	case strings.HasSuffix(src, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(src, ".avif"):
		return "image/avif"
	default:
		// Covers .jpg/.jpeg and anything unrecognized. A wrong-but-plausible
		// type is better than omitting the element a reader needs to show a
		// thumbnail at all.
		return "image/jpeg"
	}
}
