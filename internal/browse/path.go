package browse

import (
	"fmt"
	"net/url"
	"strings"
)

// ParsePath accepts one literal path or the shell quoting terminals commonly
// apply to dropped files. It never expands variables or executes shell syntax.
// Unquoted paths containing spaces are accepted too (e.g. Finder's Copy Path).
func ParsePath(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("enter one file or directory path")
	}
	if strings.ContainsAny(s, "\x00\r\n") {
		return "", fmt.Errorf("enter one path on a single line")
	}
	if s[0] == '\'' || s[0] == '"' || strings.Contains(s, `\`) {
		var b strings.Builder
		var quote byte
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == '\\' && quote != '\'' {
				if i+1 == len(s) {
					return "", fmt.Errorf("incomplete path escape")
				}
				next := s[i+1]
				// In double quotes a backslash only quotes these shell characters.
				if quote == '"' && !strings.ContainsRune("\\\"$`", rune(next)) {
					b.WriteByte(c)
					continue
				}
				b.WriteByte(next)
				i++
			} else if quote != 0 {
				if c == quote {
					quote = 0
				} else {
					b.WriteByte(c)
				}
			} else if c == '\'' || c == '"' {
				quote = c
			} else if c == ' ' || c == '\t' {
				return "", fmt.Errorf("choose one path at a time; quote or escape spaces")
			} else {
				b.WriteByte(c)
			}
		}
		if quote != 0 {
			return "", fmt.Errorf("unclosed path quote")
		}
		s = b.String()
	}
	if strings.HasPrefix(s, "file://") {
		u, err := url.Parse(s)
		if err != nil || (u.Host != "" && u.Host != "localhost") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("expected a local file:// URL")
		}
		s = u.Path
	}
	if s == "" || strings.ContainsAny(s, "\x00\r\n") {
		return "", fmt.Errorf("enter one path on a single line")
	}
	return s, nil
}
