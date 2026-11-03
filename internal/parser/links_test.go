package parser

import (
	"strings"
	"testing"
)

func TestExtractLinks(t *testing.T) {
	htmlContent := `
<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
    <a href="/about">About Us</a>
    <a href="contact.html">Contact</a>
    <a href="https://example.com/products">Products</a>
    <a href="https://otherdomain.com/ad">External Ad</a>
    <a href="#section2">Internal Anchor</a>
    <a href="mailto:admin@example.com">Email Us</a>
    <a href="javascript:void(0)">Click Here</a>
    <a href="tel:+123456789">Call Us</a>
    <a href="/about">Duplicate About Link</a>
</body>
</html>
`

	baseURL := "https://example.com/section/index.html"

	t.Run("SameDomainOnly = true", func(t *testing.T) {
		links, err := ExtractLinks(strings.NewReader(htmlContent), baseURL, ParseOptions{
			SameDomainOnly: true,
		})
		if err != nil {
			t.Fatalf("ExtractLinks failed: %v", err)
		}

		expected := []string{
			"https://example.com/about",
			"https://example.com/section/contact.html",
			"https://example.com/products",
		}

		if len(links) != len(expected) {
			t.Fatalf("expected %d links, got %d: %v", len(expected), len(links), links)
		}

		for i, exp := range expected {
			if links[i] != exp {
				t.Errorf("link[%d] = %q, expected %q", i, links[i], exp)
			}
		}
	})

	t.Run("SameDomainOnly = false", func(t *testing.T) {
		links, err := ExtractLinks(strings.NewReader(htmlContent), baseURL, ParseOptions{
			SameDomainOnly: false,
		})
		if err != nil {
			t.Fatalf("ExtractLinks failed: %v", err)
		}

		// Should include external domain as well
		foundExternal := false
		for _, l := range links {
			if l == "https://otherdomain.com/ad" {
				foundExternal = true
				break
			}
		}

		if !foundExternal {
			t.Errorf("expected external link https://otherdomain.com/ad to be included when SameDomainOnly is false")
		}
	})
}

