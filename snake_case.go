package zip

import "strings"

func snakeCase(s string) string {
	var b strings.Builder
	runes := []rune(s)
	n := len(runes)
	for i := 0; i < n; i++ {
		r := runes[i]
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				prev := runes[i-1]
				next := rune(0)
				if i+1 < n {
					next = runes[i+1]
				}
				if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') ||
					(prev >= 'A' && prev <= 'Z' && next >= 'a' && next <= 'z') {
					b.WriteByte('_')
				}
			}
			b.WriteRune(r + ('a' - 'A'))
		} else if r == '-' || r == '.' || r == '/' || r == '_' {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	res := b.String()
	for strings.Contains(res, "__") {
		res = strings.ReplaceAll(res, "__", "_")
	}
	return strings.Trim(res, "_")
}
