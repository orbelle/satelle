package config

import (
	"strings"
)

func principleTagsLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "tags:") {
			return line
		}
	}
	return ""
}
