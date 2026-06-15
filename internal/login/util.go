package login

import "strings"

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func chooseFile(v1, v2 string) string {
	if v1 != "" {
		return v1
	}
	return v2
}
