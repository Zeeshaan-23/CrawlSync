package urlmanager

import (
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "Lowercase scheme and host",
			input:    "HTTP://EXAMPLE.COM/path",
			expected: "http://example.com/path",
			wantErr:  false,
		},
		{
			name:     "Strip fragment",
			input:    "https://example.com/about#team",
			expected: "https://example.com/about",
			wantErr:  false,
		},
		{
			name:     "Strip default HTTP port 80",
			input:    "http://example.com:80/home",
			expected: "http://example.com/home",
			wantErr:  false,
		},
		{
			name:     "Strip default HTTPS port 443",
			input:    "https://example.com:443/home",
			expected: "https://example.com/home",
			wantErr:  false,
		},
		{
			name:     "Keep non-default port",
			input:    "http://example.com:8080/home",
			expected: "http://example.com:8080/home",
			wantErr:  false,
		},
		{
			name:     "Empty path normalized to slash",
			input:    "https://example.com",
			expected: "https://example.com/",
			wantErr:  false,
		},
		{
			name:     "Root path preserves single slash",
			input:    "https://example.com/",
			expected: "https://example.com/",
			wantErr:  false,
		},
		{
			name:     "Trailing slash stripped for non-root path",
			input:    "https://example.com/blog/",
			expected: "https://example.com/blog",
			wantErr:  false,
		},
		{
			name:     "Resolve dot and dot-dot path segments",
			input:    "https://example.com/a/b/../c/./d",
			expected: "https://example.com/a/c/d",
			wantErr:  false,
		},
		{
			name:     "Deterministic query parameter sorting",
			input:    "https://example.com/search?z=1&a=2&m=hello",
			expected: "https://example.com/search?a=2&m=hello&z=1",
			wantErr:  false,
		},
		{
			name:     "Unsupported scheme rejected",
			input:    "ftp://example.com/file",
			expected: "",
			wantErr:  true,
		},
		{
			name:     "Empty string rejected",
			input:    "   ",
			expected: "",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := NormalizeURL(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NormalizeURL(%q) err = %v, wantErr = %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && actual != tc.expected {
				t.Errorf("NormalizeURL(%q) = %q, expected %q", tc.input, actual, tc.expected)
			}
		})
	}
}

