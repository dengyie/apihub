package common

import (
	"strings"
	"testing"
)

const pageWithPlaceholders = `<html>
<!--umami-->
<!--Google Analytics-->
</html>`

func TestInjectAnalytics(t *testing.T) {
	t.Run("leaves only the disambiguated comments when nothing is configured", func(t *testing.T) {
		t.Setenv("UMAMI_WEBSITE_ID", "")
		t.Setenv("GOOGLE_ANALYTICS_ID", "")

		got := string(InjectAnalytics([]byte(pageWithPlaceholders)))

		// `<!--umami-->` is lower case in the build output and upper case in the
		// substituted marker, so this one is a real assertion: the build-time
		// placeholder must not survive. The google marker is spelled identically
		// to its placeholder, so with no ID configured the substitution is a
		// byte-for-byte no-op — what we can assert is that it is exactly the
		// pre-existing behaviour, not an empty line or a dropped slot.
		if strings.Contains(got, "<!--umami-->") {
			t.Error("the build-time umami placeholder must never reach a browser")
		}
		want := strings.ReplaceAll(pageWithPlaceholders, "<!--umami-->", "<!--Umami-->")
		if got != want {
			t.Errorf("unconfigured injection must be a no-op apart from the marker:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("injects umami when configured", func(t *testing.T) {
		t.Setenv("UMAMI_WEBSITE_ID", "site-123")
		t.Setenv("UMAMI_SCRIPT_URL", "")
		t.Setenv("GOOGLE_ANALYTICS_ID", "")

		got := string(InjectAnalytics([]byte(pageWithPlaceholders)))

		if !strings.Contains(got, `data-website-id="site-123"`) {
			t.Errorf("umami snippet missing, got %q", got)
		}
		if !strings.Contains(got, "https://analytics.umami.is/script.js") {
			t.Errorf("umami should default its script URL, got %q", got)
		}
	})

	t.Run("honours a custom umami script url", func(t *testing.T) {
		t.Setenv("UMAMI_WEBSITE_ID", "site-123")
		t.Setenv("UMAMI_SCRIPT_URL", "https://stats.example.com/s.js")

		got := string(InjectAnalytics([]byte(pageWithPlaceholders)))

		if !strings.Contains(got, "https://stats.example.com/s.js") {
			t.Errorf("custom script URL ignored, got %q", got)
		}
	})

	t.Run("injects google analytics when configured", func(t *testing.T) {
		t.Setenv("UMAMI_WEBSITE_ID", "")
		t.Setenv("GOOGLE_ANALYTICS_ID", "G-ABC123")

		got := string(InjectAnalytics([]byte(pageWithPlaceholders)))

		if !strings.Contains(got, "gtag/js?id=G-ABC123") {
			t.Errorf("google snippet missing, got %q", got)
		}
		if !strings.Contains(got, "gtag('config', 'G-ABC123');") {
			t.Errorf("google config call missing, got %q", got)
		}
	})

	t.Run("does not mutate its input", func(t *testing.T) {
		t.Setenv("UMAMI_WEBSITE_ID", "site-123")
		original := []byte(pageWithPlaceholders)
		InjectAnalytics(original)
		if string(original) != pageWithPlaceholders {
			t.Error("InjectAnalytics must not edit the caller's slice")
		}
	})
}
