package extract

import (
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"rss-workshop/internal/model"
)

const svgPlaceholder = "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciLz4="

var imageTestTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// imageElement parses one tag and returns its first element child of body.
func imageElement(t *testing.T, markup string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	nodes := selectNodes(doc, "css", "body > *")
	if len(nodes) == 0 {
		t.Fatalf("no element in %q", markup)
	}
	return nodes[0]
}

func imageTestBase(t *testing.T) *url.URL {
	t.Helper()
	base, err := url.Parse("https://example.com/news/index.html")
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func lazyImage(attributes string) string {
	return `<img src="` + svgPlaceholder + `" ` + attributes + `>`
}

func TestAutomaticImageDetection(t *testing.T) {
	base := imageTestBase(t)
	for _, tc := range []struct{ name, markup, want string }{
		// Images that already resolve keep the address they always had.
		{"src wins over srcset", `<img src="/real.jpg" srcset="/small.jpg 1x, /large.jpg 2x">`, "https://example.com/real.jpg"},
		{"src wins over other attributes", `<img src="/real.jpg" data-webp="/other.webp" data-original="/third.jpg">`, "https://example.com/real.jpg"},
		{"data-src wins over src", `<img src="/real.jpg" data-src="/lazy.jpg">`, "https://example.com/lazy.jpg"},
		{"data-lazy-src wins over src", `<img src="/real.jpg" data-lazy-src="/lazy.jpg">`, "https://example.com/lazy.jpg"},
		{"data-src wins over data-lazy-src", `<img data-src="/a.jpg" data-lazy-src="/b.jpg">`, "https://example.com/a.jpg"},
		{"srcset is used after src", `<img srcset="/small.jpg 1x, /large.jpg 2x">`, "https://example.com/large.jpg"},
		{"data-srcset wins over srcset", `<img data-srcset="/a.jpg 1x" srcset="/b.jpg 1x">`, "https://example.com/a.jpg"},
		{"srcset wins over a lazy attribute", `<img srcset="/real.jpg 1x" data-webp="/other.webp">`, "https://example.com/real.jpg"},
		{"srcset wins over a lazy srcset", `<img srcset="/real.jpg 1x" data-lazy-srcset="/other.jpg 1x">`, "https://example.com/real.jpg"},
		{"srcset needs no image address", `<img srcset="small 1x, large 2x">`, "https://example.com/news/large"},

		// A placeholder in src falls back to the attribute a script reads.
		{"no src at all", `<img data-original="/1.jpg">`, "https://example.com/1.jpg"},
		{"network path", lazyImage(`data-orig-src="//cdn.example.com/1.jpg"`), "https://cdn.example.com/1.jpg"},
		{"bare relative path with image extension", lazyImage(`data-img="thumbs/1.webp"`), "https://example.com/news/thumbs/1.webp"},
		{"dot-relative path", lazyImage(`data-img="./1"`), "https://example.com/news/1"},
		{"parent-relative path", lazyImage(`data-img="../1"`), "https://example.com/1"},
		{"extensionless CDN address", lazyImage(`data-image="https://cdn.example.com/i/42?w=800"`), "https://cdn.example.com/i/42?w=800"},
		{"extensionless address over http", lazyImage(`data-image="http://cdn.example.com/i/42"`), "http://cdn.example.com/i/42"},
		{"script-generated thumbnail", lazyImage(`data-image="/thumb.php?id=1"`), "https://example.com/thumb.php?id=1"},
		{"image ending in a slash", lazyImage(`data-image="/get_file/1.jpg/"`), "https://example.com/get_file/1.jpg/"},
		{"comma inside an address", lazyImage(`data-image="/w_400,h_300/1.jpg"`), "https://example.com/w_400,h_300/1.jpg"},
		{"surrounding whitespace", lazyImage(`data-original=" /1.jpg "`), "https://example.com/1.jpg"},
		{"srcset list in a script's attribute", lazyImage(`data-lazy-srcset="/s.jpg 400w, /l.jpg 800w"`), "https://example.com/l.jpg"},
		{"skips a flag to reach an address", `<img data-lazy="true" src="` + svgPlaceholder + `" data-webp="/1.webp">`, "https://example.com/1.webp"},
		{"skips a srcset flag to reach a list", lazyImage(`data-lazy-srcset="true" nitro-lazy-srcset="/a.jpg 1x"`), "https://example.com/a.jpg"},
		{"path that merely starts like base64", lazyImage(`data-image="/9jokes/a.jpg"`), "https://example.com/9jokes/a.jpg"},
		{"upper-case path that merely starts like base64", lazyImage(`data-image="/9J/4AAQ"`), "https://example.com/9J/4AAQ"},
		{"slash in the query of an address", lazyImage(`data-image="/thumb?next=/"`), "https://example.com/thumb?next=/"},
		{"video preview beside the real address", lazyImage(`data-preview="https://example.com/get_file/clip.mp4/" data-webp="/1.jpg"`), "https://example.com/1.jpg"},
		{"link beside the real address", lazyImage(`data-image-link="/story/42" data-webp="/1.webp"`), "https://example.com/1.webp"},
		{"skips a srcset candidate that is not an address", lazyImage(`data-lazy-srcset="/s.jpg 400w, true"`), "https://example.com/s.jpg"},

		// The list decides between attributes, whatever order the tag has them in.
		{"original before a script's attribute", lazyImage(`data-lazy="/l.jpg" data-original="/o.jpg"`), "https://example.com/o.jpg"},
		{"original before a generic name", lazyImage(`data-image="/i.jpg" data-original="/o.jpg"`), "https://example.com/o.jpg"},
		{"generic name before a format variant", lazyImage(`data-webp="/w.webp" data-image="/i.jpg"`), "https://example.com/i.jpg"},
		{"WebP before AVIF", lazyImage(`data-avif="/a.avif" data-webp="/w.webp"`), "https://example.com/w.webp"},
		{"format variant before a thumbnail", lazyImage(`data-thumb="/t.jpg" data-webp="/w.webp"`), "https://example.com/w.webp"},
		{"single address before a srcset list", lazyImage(`data-lazy-srcset="/s.jpg 1x" data-original="/o.jpg"`), "https://example.com/o.jpg"},

		// Nothing here is an image address, so no image is better than a wrong one.
		{"placeholder alone", lazyImage(``), ""},
		{"placeholder in every attribute", lazyImage(`data-webp="` + svgPlaceholder + `"`), ""},

		// A name that merely mentions an image says nothing about the value, so
		// these values all look like addresses and are still refused.
		{"preview", lazyImage(`data-preview="/1.jpg"`), ""},
		{"video player", lazyImage(`data-video-src="https://www.youtube.com/embed/abc"`), ""},
		{"video stream", lazyImage(`data-video-src="/stream/42"`), ""},
		{"audio", lazyImage(`data-audio-src="/clip"`), ""},
		{"iframe", lazyImage(`data-iframe-src="https://player.example.com/embed/1"`), ""},
		{"link", lazyImage(`data-image-link="https://example.com/story/42"`), ""},
		{"page", lazyImage(`data-original-page="/story"`), ""},
		{"source site", lazyImage(`data-original-url="/article"`), ""},
		{"fallback image", lazyImage(`data-fallback-src="/no-image.png"`), ""},
		{"default image", lazyImage(`data-default-image="/default.png"`), ""},
		{"spinner", lazyImage(`data-loading-src="/spinner.gif"`), ""},
		{"placeholder address by name", lazyImage(`data-placeholder-src="/spacer.gif"`), ""},
		{"script", lazyImage(`data-lazy-script="/static/foo"`), ""},
		{"tracker", lazyImage(`data-lazy-track="https://ads.example.com/pixel?id=1"`), ""},
		{"tooltip naming a file", lazyImage(`data-original-title="/photo.jpg"`), ""},
		{"title naming a file", lazyImage(`data-image-title="sunset.jpg"`), ""},
		{"caption", lazyImage(`data-image-caption="/cap"`), ""},
		{"identifier", lazyImage(`data-image-id="/42"`), ""},
		{"base path", lazyImage(`data-img-path="/uploads/x"`), ""},
		{"file extension setting", lazyImage(`data-img-ext="/x.jpg"`), ""},
		{"framework binding", lazyImage(`ng-src="/img/1.jpg"`), ""},

		// A listed attribute's value still has to be one image address.
		{"flag", lazyImage(`data-lazy="true"`), ""},
		{"count", lazyImage(`data-image="42"`), ""},
		{"bare word", lazyImage(`data-img="sunset"`), ""},
		{"text naming a file", lazyImage(`data-image="Photo.jpg by Alice"`), ""},
		{"text starting with a path", lazyImage(`data-image="/r/pics and more"`), ""},
		{"non-breaking space", lazyImage("data-image=\"/a b.jpg\""), ""},
		{"upper-case video extension", lazyImage(`data-image="/CLIP.MP4"`), ""},
		{"video ending in a slash", lazyImage(`data-image="https://example.com/get_file/clip.mp4/"`), ""},
		{"site root", lazyImage(`data-image="/"`), ""},
		{"current directory", lazyImage(`data-image="/."`), ""},
		{"parent directory", lazyImage(`data-image="/a/.."`), ""},
		{"empty network path", lazyImage(`data-original="//"`), ""},
		{"host alone", lazyImage(`data-image="https://cdn.example.com"`), ""},
		{"directory on a host", lazyImage(`data-image="https://cdn.example.com/images/"`), ""},
		{"relative directory", lazyImage(`data-image="./"`), ""},
		{"bare extension", lazyImage(`data-image=".jpg"`), ""},
		{"extension with no file name", lazyImage(`data-image="/.webp"`), ""},
		{"raw base64 JPEG", lazyImage(`data-image="/9j/4AAQSkZJRgABAQAAAQABAAD"`), ""},
		{"raw base64 JPEG with other bytes", lazyImage(`data-image="/9j/2wCE"`), ""},
		{"decimal number", lazyImage(`data-image=".5"`), ""},
		{"dots naming nothing", lazyImage(`data-image="..foo"`), ""},
		{"long fragment hiding the length", lazyImage(`data-original="/a.jpg#` + strings.Repeat("f", 8200) + `"`), ""},
		{"leading comma", lazyImage(`data-image=",/a.jpg"`), ""},
		{"trailing comma", lazyImage(`data-image="/a.jpg,"`), ""},
		{"network path that does not resolve to a valid address", lazyImage(`data-original="//::/a.jpg"`), ""},
		{"same in a srcset list", lazyImage(`data-lazy-srcset="//::/a.jpg 1x"`), ""},
		{"list joined by a comma", lazyImage(`data-image="/a.jpg,/b.jpg"`), ""},
		{"absolute list joined by a comma", lazyImage(`data-image="https://cdn.example.com/1.jpg,https://cdn.example.com/2.jpg"`), ""},
		{"list of bare files", lazyImage(`data-image="a.jpg,b.jpg"`), ""},
		{"list joined by a pipe", lazyImage(`data-image="/a.jpg|/b.jpg"`), ""},
		{"template", lazyImage(`data-image="/img/{{item.id}}.jpg"`), ""},
		{"opening brace", lazyImage(`data-image="/a{b.jpg"`), ""},
		{"closing brace", lazyImage(`data-image="/a}b.jpg"`), ""},
		{"less-than sign", lazyImage(`data-image="/a&lt;b.jpg"`), ""},
		{"greater-than sign", lazyImage(`data-image="/a&gt;b.jpg"`), ""},
		{"double quote", lazyImage(`data-image='/a"b.jpg'`), ""},
		{"single quote", lazyImage(`data-image="/a'b.jpg"`), ""},
		{"invalid escape", lazyImage(`data-image="/a%zz.jpg"`), ""},
		{"truncated escape", lazyImage(`data-image="/%"`), ""},
		{"invalid host", lazyImage(`data-image="http://[::1"`), ""},
		{"invalid escape in a srcset", lazyImage(`data-lazy-srcset="/a%zz.jpg 1x"`), ""},
		{"unsafe scheme", lazyImage(`data-original="javascript:alert(1)"`), ""},
		{"unsafe scheme naming an image", lazyImage(`data-original="javascript:alert(1).jpg"`), ""},
		{"credentials in the address", lazyImage(`data-original="https://user:secret@example.com/1.jpg"`), ""},
		{"srcset flag", lazyImage(`data-lazy-srcset="true"`), ""},
		{"srcset of video", lazyImage(`data-lazy-srcset="/a.mp4 1x, /b.mp4 2x"`), ""},

		// Other elements can reach detection through a selector, and on them the
		// same names hold links and streams.
		{"attribute on a video", `<video data-original="/stream/42"></video>`, ""},
		{"attribute on a link", `<a data-original="https://publisher.example/story/42"></a>`, ""},
		{"attribute on an iframe", `<iframe data-ezsrc="https://www.youtube.com/embed/abc"></iframe>`, ""},
		{"attribute on a block", `<div data-lazy="/ajax/media/42"></div>`, ""},
		{"attribute on a section", `<section data-original="/story/42"></section>`, ""},
		{"attribute on a figure", `<figure data-image="/story/42"></figure>`, ""},
		{"attribute on a span", `<span data-lazy="/ajax/42"></span>`, ""},
		{"attribute on a picture", `<picture data-webp="/a.webp"></picture>`, ""},
		{"attribute on audio", `<audio data-original="/stream/42"></audio>`, ""},
		{"attribute on an embed", `<embed data-original="/stream/42">`, ""},
		{"attribute on a source", `<source data-original="/clip.jpg">`, ""},
		{"usual attribute on a block is unchanged", `<div data-src="/1.jpg"></div>`, "https://example.com/1.jpg"},

		// srcset candidates split as browsers split them, keeping the addresses
		// sites publish with a bare comma between them.
		{"comma inside a CDN address", `<img srcset="https://cdn.example.com/w_400,h_300/a.jpg 400w, https://cdn.example.com/w_800,h_600/a.jpg 800w">`, "https://cdn.example.com/w_800,h_600/a.jpg"},
		{"comma before a file name", `<img srcset="/c,d.jpg 2x">`, "https://example.com/c,d.jpg"},
		{"no space after the separating comma", `<img srcset="/a.jpg 1x,/b.jpg 2x">`, "https://example.com/b.jpg"},
		{"trailing comma ends a candidate", `<img srcset="/a.jpg, /b.jpg,">`, "https://example.com/b.jpg"},
		{"leading and doubled commas", `<img srcset=",/a, /b,,">`, "https://example.com/b"},
		{"joined paths", `<img srcset="/a,/b">`, "https://example.com/b"},
		{"three joined paths", `<img srcset="/a,/b,/c">`, "https://example.com/c"},
		{"unusable address after joined files", `<img srcset="/a.jpg,/b.jpg,data:x">`, "https://example.com/b.jpg"},
		{"a directory named like a data: URI", `<img srcset="data/a.jpg,/b.jpg">`, "https://example.com/b.jpg"},
		{"scheme with no address after a comma", `<img srcset="/a,http://">`, "https://example.com/a"},
		{"http: alone does not start an address", `<img srcset="/a,http:x">`, "https://example.com/a,http:x"},
		{"https: alone does not start an address", `<img srcset="/a,https:x">`, "https://example.com/a,https:x"},
		{"leading comma before a file", `<img srcset=",a.jpg 1x">`, "https://example.com/news/a.jpg"},
		{"non-breaking space before an address", "<img srcset=\"\u00a0/a.jpg 2x\">", "https://example.com/a.jpg"},
		{"descriptors run to the next comma", `<img srcset="/a.jpg 1x /b.jpg 2x">`, "https://example.com/a.jpg"},
		{"joined http addresses", `<img srcset="http://cdn.example.com/a,http://cdn.example.com/b">`, "http://cdn.example.com/b"},
		{"joined https addresses", `<img srcset="https://cdn.example.com/a,https://cdn.example.com/b 2x">`, "https://cdn.example.com/b"},
		{"joined addresses in upper case", `<img srcset="HTTPS://cdn.example.com/a,HTTPS://cdn.example.com/b">`, "https://cdn.example.com/b"},
		{"joined files in upper case", `<img data-srcset="A.JPG,B.JPG">`, "https://example.com/news/B.JPG"},
		{"non-breaking space before a descriptor", "<img srcset=\"/a.jpg 1x, /b.jpg 2x\">", "https://example.com/b.jpg"},
		{"data: URI in srcset is not split into a path", `<img srcset="` + svgPlaceholder + ` 1x">`, ""},
		{"JPEG data: URI is not split into a path", `<img srcset="data:image/jpeg;base64,/9j/4AAQSkZJRg 1x">`, ""},
		{"upper-case data: URI is not split into a path", `<img srcset="DATA:image/jpeg;base64,/9j/4AAQSkZJRg 1x">`, ""},
		{"data: URI after a real candidate", `<img srcset="/a.jpg 1x, ` + svgPlaceholder + ` 2x">`, "https://example.com/a.jpg"},
		{"whitespace only", `<img srcset="  , ">`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := itemImageURL(imageElement(t, tc.markup), base); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Every attribute operators are told about works on its own.
func TestDocumentedLazyImageAttributes(t *testing.T) {
	base := imageTestBase(t)
	for _, name := range []string{
		"data-original", "data-orig-src", "data-orig-file", "data-original-src", "data-lazy-original", "data-wpfc-original-src",
		"data-lazy", "data-lazyload", "data-flickity-lazyload", "data-flickity-lazyload-src", "data-echo", "data-breeze", "nitro-lazy-src", "data-pagespeed-lazy-src", "data-cfsrc", "data-ezsrc", "data-actualsrc",
		"data-image", "data-image-src", "data-img", "data-img-src", "data-img-url",
		"data-webp", "data-avif", "data-thumb",
	} {
		if got := itemImageURL(imageElement(t, lazyImage(name+`="/listed.jpg"`)), base); got != "https://example.com/listed.jpg" {
			t.Errorf("%s: got %q", name, got)
		}
	}
	for _, name := range []string{"data-lazy-srcset", "data-original-srcset", "data-original-set", "data-flickity-lazyload-srcset", "data-brsrcset", "nitro-lazy-srcset"} {
		if got := itemImageURL(imageElement(t, lazyImage(name+`="/small.jpg 1x, /listed.jpg 2x"`)), base); got != "https://example.com/listed.jpg" {
			t.Errorf("%s: got %q", name, got)
		}
	}
	guide, err := os.ReadFile("../../docs/visual-selector.md")
	if err != nil {
		t.Fatal(err)
	}
	rest := string(guide)
	for _, name := range append(append(append([]string(nil), imageAttributes...), lazyImageAttributes...), lazySrcsetAttributes...) {
		_, after, found := strings.Cut(rest, "`"+name+"`")
		if !found {
			t.Fatalf("docs/visual-selector.md does not list %s in detection order", name)
		}
		rest = after
	}
}

func TestLazyImageAddressExtensions(t *testing.T) {
	base := imageTestBase(t)
	for _, ext := range []string{
		"mp4", "m4v", "webm", "mov", "ogv", "mkv", "avi", "wmv", "flv", "mpg", "mpeg", "ts", "3gp", "m3u8", "mpd", "m4s",
		"mp3", "m4a", "ogg", "oga", "opus", "wav", "flac", "aac",
		"pdf", "html", "htm", "js", "mjs", "css", "json", "xml", "txt", "zip",
	} {
		if got := itemImageURL(imageElement(t, lazyImage(`data-image="/x.`+ext+`"`)), base); got != "" {
			t.Errorf(".%s published as an image: %q", ext, got)
		}
	}
	for _, ext := range []string{"jpg", "jpeg", "jfif", "png", "gif", "webp", "avif", "heic", "svg", "bmp", "tif", "tiff"} {
		// A bare relative path needs one of these, and a bare comma after one
		// separates two addresses.
		want := "https://example.com/news/b." + ext
		if got := itemImageURL(imageElement(t, lazyImage(`data-img="b.`+ext+`"`)), base); got != want {
			t.Errorf(".%s: got %q", ext, got)
		}
		if got := itemImageURL(imageElement(t, `<img srcset="a.`+ext+`,b.`+ext+`">`), base); got != want {
			t.Errorf("joined .%s: got %q", ext, got)
		}
	}
}

// The shape of a real page: early stories carry the address in src, later ones
// only a placeholder with the address in data-webp, beside a video preview.
func imageCards() string {
	card := func(n, src string) string {
		return `<div class="card"><a href="/video/` + n + `/" title="Post ` + n + `"><img class="thumb " loading="lazy"src="` + src + `" data-webp="https://cdn.example.com/` + n + `.jpg" alt="Post ` + n + `" data-preview="https://example.com/get_file/` + n + `_preview.mp4/"></a></div>`
	}
	return `<img src="/logo.svg" alt="Logo">` + card("1", "https://cdn.example.com/1.jpg") + card("2", svgPlaceholder) + card("3", svgPlaceholder)
}

func TestLazyImagesThroughExtraction(t *testing.T) {
	want := []string{"https://cdn.example.com/1.jpg", "https://cdn.example.com/2.jpg", "https://cdn.example.com/3.jpg"}
	for _, tc := range []struct {
		name, typ, items, title, link string
		image                         model.Field
	}{
		{"css with no image selector", "css", ".card", "a", "a", model.Field{}},
		{"css image selector", "css", ".card", "a", "a", model.Field{Selector: "img"}},
		{"css explicit attribute", "css", ".card", "a", "a", model.Field{Selector: "img", Attr: "data-webp"}},
		{"xpath image selector", "xpath", "//div[@class='card']", ".//a", ".//a", model.Field{Selector: ".//img"}},
		{"xpath attribute result", "xpath", "//div[@class='card']", ".//a", ".//a", model.Field{Selector: ".//img/@data-webp"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := model.Recipe{Type: tc.typ, Items: tc.items, Title: model.Field{Selector: tc.title, Attr: "title"}, Link: model.Field{Selector: tc.link}, Image: tc.image}
			p, err := RunAt([]byte(imageCards()), "https://example.com/", r, imageTestTime)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Items) != len(want) || len(p.Warnings) != 0 {
				t.Fatalf("items %+v, warnings %q", p.Items, p.Warnings)
			}
			for i, item := range p.Items {
				if item.Image != want[i] {
					t.Fatalf("story %d image %q, want %q", i+1, item.Image, want[i])
				}
			}
		})
	}
}

func extractImageCard(t *testing.T, card string, image model.Field) model.Preview {
	t.Helper()
	r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Image: image}
	p, err := RunAt([]byte(`<article><h2>Story</h2><a href="/story">Read</a>`+card+`</article>`), "https://example.com/", r, imageTestTime)
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("items %+v, err %v", p.Items, err)
	}
	return p
}

// Only the first matched image is read, as a later one is as likely to be an
// avatar, a play icon or a tracking pixel as the story's image. The warning that
// tells the operator to narrow the selector or name an attribute stays.
func TestOnlyTheFirstMatchedImageIsRead(t *testing.T) {
	placeholderThenAvatar := `<img class="thumb" src="` + svgPlaceholder + `"><img class="avatar" src="/avatars/alice.jpg">`
	if p := extractImageCard(t, placeholderThenAvatar, model.Field{}); p.Items[0].Image != "" || len(p.Warnings) != 0 {
		t.Fatalf("blank selector: image %q, warnings %q", p.Items[0].Image, p.Warnings)
	}
	for _, image := range []model.Field{{Selector: "img"}, {Selector: "img", Attr: "src"}} {
		p := extractImageCard(t, placeholderThenAvatar, image)
		if p.Items[0].Image != "" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], `received URL "data:image/svg+xml`) {
			t.Fatalf("%+v: image %q, warnings %q", image, p.Items[0].Image, p.Warnings)
		}
	}
	if p := extractImageCard(t, `<img src="/first.jpg"><img src="/second.jpg">`, model.Field{Selector: "img"}); p.Items[0].Image != "https://example.com/first.jpg" {
		t.Fatalf("image %q", p.Items[0].Image)
	}
	// A narrower selector reaches the later image.
	if p := extractImageCard(t, placeholderThenAvatar, model.Field{Selector: "img.avatar"}); p.Items[0].Image != "https://example.com/avatars/alice.jpg" {
		t.Fatalf("image %q", p.Items[0].Image)
	}
}

// When detection finds nothing, the warning shows what the element held, so the
// operator can name the attribute that has the address.
func TestImageWarningReportsTheUsualAttributes(t *testing.T) {
	for _, name := range []string{"data-src", "data-lazy-src", "src", "data-srcset", "srcset"} {
		p := extractImageCard(t, `<img `+name+`="`+svgPlaceholder+`">`, model.Field{Selector: "img"})
		if p.Items[0].Image != "" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], `received URL "data:image/svg+xml`) {
			t.Fatalf("%s: image %q, warnings %q", name, p.Items[0].Image, p.Warnings)
		}
	}
	for _, tc := range []struct{ card, want string }{
		// The first usual attribute with a value is the one reported.
		{`<img data-src="data:a" src="data:b">`, `received URL "data:a"`},
		// A blank value is no value.
		{`<img data-src=" " src="` + svgPlaceholder + `">`, `received URL "data:image/svg+xml`},
		// Lazy attributes are not reported, as they may not be meant as images.
		{`<img data-webp="/a b.mp4">`, `selected element has no "src" attribute`},
	} {
		p := extractImageCard(t, tc.card, model.Field{Selector: "img"})
		if p.Items[0].Image != "" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], tc.want) {
			t.Fatalf("%s: image %q, warnings %q", tc.card, p.Items[0].Image, p.Warnings)
		}
	}
	// A selector matching nothing reports it rather than using the first image.
	p := extractImageCard(t, `<img src="/x.jpg">`, model.Field{Selector: ".nomatch"})
	if p.Items[0].Image != "" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "selector matched no nodes") {
		t.Fatalf("image %q, warnings %q", p.Items[0].Image, p.Warnings)
	}
	for _, card := range []string{`<video class="clip" data-original="/stream/42"></video>`, `<a class="clip" data-original="/story/42"></a>`} {
		p := extractImageCard(t, card, model.Field{Selector: ".clip"})
		if p.Items[0].Image != "" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "image omitted") {
			t.Fatalf("%s: image %q, warnings %q", card, p.Items[0].Image, p.Warnings)
		}
	}
}

// An explicit attribute is the operator's choice and reads nothing else.
func TestExplicitImageAttributeReadsOnlyThatAttribute(t *testing.T) {
	image := model.Field{Selector: "img", Attr: "data-webp"}
	if p := extractImageCard(t, `<img src="/real.jpg" data-webp="/chosen.webp">`, image); p.Items[0].Image != "https://example.com/chosen.webp" {
		t.Fatalf("image %q", p.Items[0].Image)
	}
	p := extractImageCard(t, `<img src="/real.jpg">`, image)
	if p.Items[0].Image != "" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], `"data-webp"`) {
		t.Fatalf("image %q, warnings %q", p.Items[0].Image, p.Warnings)
	}
}

// The inline image is built from text a source page controls.
func TestInlineImageAddressIsEscaped(t *testing.T) {
	for _, tc := range []struct{ card, want string }{
		{`<img srcset='/l.jpg?q="&gt;&lt;script&gt;alert(1)&lt;/script&gt; 1x'>`, `<p><img src="https://example.com/l.jpg?q=&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;" alt=""></p>`},
		{lazyImage(`data-webp="/a.jpg?w=1&amp;h=2"`), `<p><img src="https://example.com/a.jpg?w=1&amp;h=2" alt=""></p>`},
	} {
		if p := extractImageCard(t, tc.card, model.Field{}); p.Items[0].HTML != tc.want {
			t.Fatalf("%s: html %q", tc.card, p.Items[0].HTML)
		}
	}
}

// The limits below are written out, because changing them changes which stories
// keep an image and which pages fail.
func TestImageAddressLimits(t *testing.T) {
	base := imageTestBase(t)
	address := func(size int) string {
		const start = "https://example.com/"
		return start + strings.Repeat("a", size-len(start))
	}
	if got := itemImageURL(imageElement(t, lazyImage(`data-original="`+address(8192)+`"`)), base); got != address(8192) {
		t.Fatalf("8192-byte lazy address: got %d bytes", len(got))
	}
	if got := itemImageURL(imageElement(t, lazyImage(`data-original="`+address(8193)+`"`)), base); got != "" {
		t.Fatalf("8193-byte lazy address: got %d bytes", len(got))
	}
	// Short enough as written, too long once percent-encoded.
	if got := itemImageURL(imageElement(t, lazyImage(`data-original="/`+strings.Repeat("é", 2000)+`.jpg"`)), base); got != "" {
		t.Fatalf("address growing past the limit: got %d bytes", len(got))
	}
	if got := itemImageURL(imageElement(t, `<img srcset="/ok.jpg 1x, `+address(8192)+` 2x">`), base); got != address(8192) {
		t.Fatalf("8192-byte srcset candidate: got %d bytes", len(got))
	}
	if got := itemImageURL(imageElement(t, `<img srcset="/ok.jpg 1x, `+address(8193)+` 2x">`), base); got != "https://example.com/ok.jpg" {
		t.Fatalf("8193-byte srcset candidate: got %d bytes", len(got))
	}

	// A value padded to an exact length: its first candidate falls out of the
	// window one byte past the limit.
	padded := func(first string, size int) string {
		const inline = ", data:image/gif;base64,"
		const end = " 2x"
		return first + " 1x" + inline + strings.Repeat("A", size-len(first)-len(" 1x")-len(inline)-len(end)) + end
	}
	if got := itemImageURL(imageElement(t, `<img srcset="`+padded("/first.jpg", 262144)+`">`), base); got != "https://example.com/first.jpg" {
		t.Fatalf("262144-byte srcset: got %q", got)
	}
	if got := itemImageURL(imageElement(t, `<img srcset="`+padded("/first.jpg", 262145)+`">`), base); got != "" {
		t.Fatalf("262145-byte srcset: got %q", got)
	}
	// The cut can fall inside a candidate, and its commas are not separators.
	for over := 1; over <= 6; over++ {
		if got := itemImageURL(imageElement(t, `<img srcset="`+padded("/w_400,h_300/a.jpg", 262144+over)+`">`), base); got != "" {
			t.Fatalf("cut %d bytes into a candidate: got %q", over, got)
		}
	}
	inline := func(size int) string { return "data:image/gif;base64," + strings.Repeat("A", size) }
	for _, tc := range []struct{ name, value, want string }{
		{"end of a long value", inline(300<<10) + ` 1x, /a.jpg 2x`, "https://example.com/a.jpg"},
		{"run ending in a comma", inline(300<<10) + `, /a.jpg 2x`, "https://example.com/a.jpg"},
		{"non-breaking space ending the run", inline(300<<10) + "\u00a01x,/y.jpg 2x", "https://example.com/y.jpg"},
		{"the first comma after the cut is the one used", inline(300<<10) + ` 1x, /y.jpg 2x, data:image/gif;base64,BBB 3x`, "https://example.com/y.jpg"},
		{"no comma leaves no candidate", "/" + strings.Repeat("A", 300<<10) + " 2x", ""},
		{"unbroken joined addresses", strings.Repeat("/a.jpg,", 50<<10) + "/z.jpg", "https://example.com/z.jpg"},
		{"cut on the separating comma", "x,y 1x, " + inline(262112) + " 2x", ""},
		{"run of one byte after the cut", "Px /y.jpg 2x, " + inline(262106) + " 2x", ""},
		{"cut exactly on a comma", "/x.jpg, /y.jpg 1x, " + inline(262106) + " 2x", "https://example.com/y.jpg"},
		{"cut just after a comma", "/x.jpg, /y.jpg 1x, " + inline(262107) + " 2x", ""},
		{"cut on the space before a comma-bearing candidate", "/p.jpg /w_400,h_300/a.jpg 1x, " + inline(262095) + " 2x", ""},
	} {
		if got := itemImageURL(imageElement(t, `<img srcset="`+tc.value+`">`), base); got != tc.want {
			t.Errorf("%s (%d bytes): got %q, want %q", tc.name, len(tc.value), got, tc.want)
		}
	}

	list := func(size int) string {
		const last = "/listed.jpg 2x"
		return strings.Repeat(" ", size-len(last)) + last
	}
	if got := itemImageURL(imageElement(t, lazyImage(`data-lazy-srcset="`+list(16384)+`"`)), base); got != "https://example.com/listed.jpg" {
		t.Fatalf("16384-byte lazy srcset: got %q", got)
	}
	if got := itemImageURL(imageElement(t, lazyImage(`data-lazy-srcset="`+list(16385)+`"`)), base); got != "" {
		t.Fatalf("16385-byte lazy srcset: got %q", got)
	}

	// A usual attribute over the limit is the operator's to see, as it always was.
	r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}}
	if _, err := RunAt([]byte(`<article><h2>One</h2><a href="/1">Read</a><img src="`+address(8192)+`"></article>`), "https://example.com/", r, imageTestTime); err != nil {
		t.Fatalf("8192-byte src: %v", err)
	}
	if _, err := RunAt([]byte(`<article><h2>One</h2><a href="/1">Read</a><img src="`+address(8193)+`"></article>`), "https://example.com/", r, imageTestTime); err == nil || !strings.Contains(err.Error(), "exceeds size limits") {
		t.Fatalf("8193-byte src: %v", err)
	}
}

// Images are optional, so a value read only as a fallback must leave the story
// and the rest of the page intact.
func TestLazyImageValuesKeepTheStories(t *testing.T) {
	long := strings.Repeat("A", 9000)
	for _, tc := range []struct{ name, image, description string }{
		{name: "raw base64 in an image attribute", image: lazyImage(`data-image="/9j/` + long + `"`)},
		{name: "over-long address", image: lazyImage(`data-original="https://example.com/r?u=` + long + `"`)},
		{name: "over-long srcset candidate", image: lazyImage(`data-lazy-srcset="/` + long + `.jpg 1x"`)},
		{name: "unbroken srcset", image: `<img srcset="` + strings.Repeat("a,", 9000) + `a">`},
		{name: "raw base64 in a description", description: lazyImage(`data-image="/9j/` + strings.Repeat("A", 300<<10) + `"`)},
		{name: "many lazy images in a description", description: strings.Repeat(lazyImage(`data-original="https://cdn.example.com/uploads/2026/11/photo-800x600.jpg"`), 3200)},
		{name: "long lazy images in a description", description: strings.Repeat(lazyImage(`data-original="/`+strings.Repeat("a", 8000)+`.jpg"`), 33)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cards := `<article><h2>One</h2><a href="/1">Read</a></article>` +
				`<article><h2>Two</h2><a href="/2">Read</a>` + tc.image + `<div class="d">` + tc.description + `<p>Text</p></div></article>` +
				`<article><h2>Three</h2><a href="/3">Read</a></article>`
			r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Content: model.Field{Selector: ".d"}}
			p, err := RunAt([]byte(cards), "https://example.com/", r, imageTestTime)
			if err != nil || len(p.Items) != 3 {
				t.Fatalf("items %d, err %v", len(p.Items), err)
			}
		})
	}
}

// Images inside a description or an article read the usual attributes only: a
// lazy image a description encloses is published once, as the story's image, and
// a placeholder never reaches a reader as a broken address.
func TestDescriptionImagesReadTheUsualAttributes(t *testing.T) {
	r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Content: model.Field{Selector: ".summary"}}
	page := `<article><h2>Story</h2><a href="/story">Read</a><div class="summary"><p>Text</p>` + lazyImage(`data-webp="/lazy.jpg"`) + `<img src="/inline.jpg"></div></article>`
	p, err := RunAt([]byte(page), "https://example.com/", r, imageTestTime)
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("items %+v, err %v", p.Items, err)
	}
	body := p.Items[0].HTML
	if p.Items[0].Image != "https://example.com/lazy.jpg" || strings.Count(body, "https://example.com/lazy.jpg") != 1 || !strings.Contains(body, "https://example.com/inline.jpg") || strings.Contains(body, "PHN2Zy") {
		t.Fatalf("image %q, description: %s", p.Items[0].Image, body)
	}
	article := `<div id="body"><p>Text</p>` + strings.Repeat(lazyImage(`data-original="https://cdn.example.com/uploads/2026/11/photo-800x600.jpg"`), 3200) + `</div>`
	got, err := Article([]byte(article), "https://example.com/story", model.Recipe{Type: "css", Full: &model.FullContent{Selector: "#body"}})
	if err != nil || strings.Contains(got, "photo-800x600") || !strings.Contains(got, "Text") {
		t.Fatalf("article err %v, %d bytes", err, len(got))
	}
}
