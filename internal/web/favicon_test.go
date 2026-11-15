package web

import (
	"strings"
	"testing"
)

func TestEmbeddedFaviconMatchesSiteMonogram(t *testing.T) {
	raw, err := staticFS.ReadFile("static/favicon.svg")
	if err != nil {
		t.Fatalf("embed missing static/favicon.svg: %v", err)
	}
	svg := string(raw)
	for _, want := range []string{
		"<circle",
		"#2f6f4f",
		"<animate",
		"prefers-reduced-motion",
		`id="static"`,
		`id="anim"`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("embedded favicon missing %q (want satelle.dev monogram):\n%s", want, svg)
		}
	}
}
