package parser

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// ParseOptions controls filtering during link extraction.
type ParseOptions struct {
	SameDomainOnly bool
}

// ExtractLinks parses an HTML document, extracts all `<a href="...">` links,
// resolves relative URLs against the provided baseURL, filters out non-HTTP schemes
// (mailto, javascript, tel, data, ftp, etc.) and in-page anchor fragments (#section),
// and optionally restricts links to the same domain as the baseURL.
func ExtractLinks(body io.Reader, baseRawURL string, opts ParseOptions) ([]string, error) {
	if body == nil {
		return nil, fmt.Errorf("body reader is nil")
	}

	base, err := url.Parse(baseRawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL %q: %w", baseRawURL, err)
	}

	tokenizer := html.NewTokenizer(body)
	var links []string
	seen := make(map[string]struct{})

	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			err := tokenizer.Err()
			if err == io.EOF {
				return links, nil
			}
			return links, fmt.Errorf("html tokenization error: %w", err)

		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()
			if token.Data != "a" {
				continue
			}

			for _, attr := range token.Attr {
				if attr.Key != "href" {
					continue
				}

				rawHref := strings.TrimSpace(attr.Val)
				if rawHref == "" || strings.HasPrefix(rawHref, "#") {
					// Skip empty or fragment-only links (#section, #top)
					continue
				}

				parsedHref, err := url.Parse(rawHref)
				if err != nil {
					// Skip malformed link
					continue
				}

				// Skip non-web schemes (mailto:, javascript:, tel:, data:, ftp:, etc.)
				scheme := strings.ToLower(parsedHref.Scheme)
				if scheme != "" && scheme != "http" && scheme != "https" {
					continue
				}

				// Resolve relative URL to absolute URL using the base URL
				resolvedURL := base.ResolveReference(parsedHref)
				resolvedURL.Fragment = "" // Strip anchor fragment

				resolvedScheme := strings.ToLower(resolvedURL.Scheme)
				if resolvedScheme != "http" && resolvedScheme != "https" {
					continue
				}

				// Enforce same-domain policy if requested
				if opts.SameDomainOnly {
					if !IsSameDomain(base.Hostname(), resolvedURL.Hostname()) {
						continue
					}
				}

				finalStr := resolvedURL.String()
				if _, alreadyFound := seen[finalStr]; !alreadyFound {
					seen[finalStr] = struct{}{}
					links = append(links, finalStr)
				}
			}
		}
	}
}

// IsSameDomain checks whether two hostnames match exactly or share the same base domain.
func IsSameDomain(baseHost, targetHost string) bool {
	baseHost = strings.ToLower(strings.TrimPrefix(baseHost, "www."))
	targetHost = strings.ToLower(strings.TrimPrefix(targetHost, "www."))
	return baseHost == targetHost
}

