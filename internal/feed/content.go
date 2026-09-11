package feed

import (
	"html"

	"rss-workshop/internal/model"
)

// itemHTML is the content both serializers publish for an item.
//
// An item's stored HTML is the list-page teaser, which already carries any
// extracted image inline. When an article body has been fetched it replaces
// that teaser, but the image is prepended the same way so a preview image the
// source published after the story went live still reaches the reader: the
// list page keeps refreshing image on every refresh, while the fetched article
// body is kept as it was retrieved.
func itemHTML(it model.Item) string {
	if it.FullHTML == "" {
		return it.HTML
	}
	if it.Image == "" {
		return it.FullHTML
	}
	return `<p><img src="` + html.EscapeString(it.Image) + `" alt=""></p>` + it.FullHTML
}
