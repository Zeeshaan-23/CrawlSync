package urlmanager

import (
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
)

// NormalizeURL canonicalizes a URL string so equivalent URLs map to identical strings.
//
// Normalization rules applied:
// 1. Parse and validate standard URL format.
// 2. Scheme and Host are lowercased (HTTP is case-insensitive for host and scheme).
// 3. Remove default ports: ":80" for http, ":443" for https.
// 4. Strip fragments (e.g. #section), as fragments represent client-side anchors, not distinct resources.
// 5. Path resolution: Resolve "." and ".." path segments using path.Clean.
// 6. Root path normalization: An empty path "" is normalized to "/".
// 7. Trailing slash policy:
//    - For the root path "/", the trailing slash is preserved.
//    - For all non-root paths (e.g. "/about/"), the trailing slash is stripped (becoming "/about").
//    - Rationale: Most web servers treat directory URLs with and without trailing slashes equivalently.
//      Stripping trailing slash prevents crawling duplicate pages under different URL aliases.
// 8. Query parameter canonicalization: Query parameters are sorted alphabetically by key
//    so "?b=1&a=2" and "?a=2&b=1" produce the identical canonical URL.
func NormalizeURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("empty URL")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("malformed URL: %w", err)
	}

	// Only http and https are allowed
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
	u.Scheme = scheme

	// Hostname normalization: lowercase and strip default port
	host := strings.ToLower(u.Host)
	if host == "" {
		return "", fmt.Errorf("missing host in URL")
	}

	// Check if port is explicitly default port
	if scheme == "http" && strings.HasSuffix(host, ":80") {
		host = strings.TrimSuffix(host, ":80")
	} else if scheme == "https" && strings.HasSuffix(host, ":443") {
		host = strings.TrimSuffix(host, ":443")
	}
	u.Host = host

	// Fragment removal: client-side anchors should not create duplicate crawl tasks
	u.Fragment = ""

	// Path normalization
	cleanPath := path.Clean(u.Path)
	if cleanPath == "." || cleanPath == "" {
		cleanPath = "/"
	} else {
		// Ensure leading slash
		if !strings.HasPrefix(cleanPath, "/") {
			cleanPath = "/" + cleanPath
		}
		// Trailing slash policy: Strip trailing slash for non-root paths
		if len(cleanPath) > 1 && strings.HasSuffix(cleanPath, "/") {
			cleanPath = strings.TrimSuffix(cleanPath, "/")
		}
	}
	u.Path = cleanPath

	// Query normalization: sort query parameters deterministically
	if u.RawQuery != "" {
		values := u.Query()
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var queryParts []string
		for _, k := range keys {
			vList := values[k]
			sort.Strings(vList)
			for _, v := range vList {
				queryParts = append(queryParts, fmt.Sprintf("%s=%s", url.QueryEscape(k), url.QueryEscape(v)))
			}
		}
		u.RawQuery = strings.Join(queryParts, "&")
	}

	return u.String(), nil
}

