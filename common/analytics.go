package common

import (
	"bytes"
	"os"
	"strings"
)

// Index-page placeholders that the build leaves in web/index.html so the
// server can substitute the snippets for the operators who have configured
// them, and a harmless comment for those who have not.
var (
	umamiPlaceholder  = []byte("<!--umami-->\n")
	googlePlaceholder = []byte("<!--Google Analytics-->\n")
)

// InjectAnalytics substitutes the analytics snippets into an index page.
//
// It is a pure function over bytes rather than a mutation of a package-level
// variable, because once the frontend can also be served from disk there is no
// single global index page any more: the embedded copy and the on-disk copy
// each need the same treatment, and the on-disk one is re-read per request so
// a rotated bundle takes effect without a restart.
//
// Both replacements always run, even with no analytics configured. That
// matches the previous behaviour: the placeholder is always consumed, so a
// page can never ship the raw build-time comment to a browser.
func InjectAnalytics(page []byte) []byte {
	return bytes.ReplaceAll(injectGoogle(page), umamiPlaceholder, umamiSnippet())
}

func injectGoogle(page []byte) []byte {
	var b strings.Builder
	if id := os.Getenv("GOOGLE_ANALYTICS_ID"); id != "" {
		b.WriteString(`<script async src="https://www.googletagmanager.com/gtag/js?id=`)
		b.WriteString(id)
		b.WriteString(`"></script>`)
		b.WriteString(`<script>`)
		b.WriteString(`window.dataLayer = window.dataLayer || [];`)
		b.WriteString(`function gtag(){dataLayer.push(arguments);}`)
		b.WriteString(`gtag('js', new Date());`)
		b.WriteString(`gtag('config', '`)
		b.WriteString(id)
		b.WriteString(`');`)
		b.WriteString(`</script>`)
	}
	b.WriteString("<!--Google Analytics-->\n")
	return bytes.ReplaceAll(page, googlePlaceholder, []byte(b.String()))
}

func umamiSnippet() []byte {
	var b strings.Builder
	if id := os.Getenv("UMAMI_WEBSITE_ID"); id != "" {
		scriptURL := os.Getenv("UMAMI_SCRIPT_URL")
		if scriptURL == "" {
			scriptURL = "https://analytics.umami.is/script.js"
		}
		b.WriteString(`<script defer src="`)
		b.WriteString(scriptURL)
		b.WriteString(`" data-website-id="`)
		b.WriteString(id)
		b.WriteString(`"></script>`)
	}
	b.WriteString("<!--Umami-->\n")
	return []byte(b.String())
}
