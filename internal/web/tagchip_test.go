package web

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestTagchipRendersKVAndBare(t *testing.T) {
	now := time.Now()
	it := Item{
		ID: "sty_1", Kind: "story", Title: "x", Status: "open", Category: "feature",
		Tags: []string{"ui", "epic:summariser"}, CreatedAt: now, UpdatedAt: now,
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "workitemRows", []rowVM{{Item: it}}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `class="tagchip clickable" data-filter="tags:ui" aria-label="filter by ui">ui</button>`) {
		t.Errorf("bare tag should render as a clickable chip; got:\n%s", out)
	}
	if !strings.Contains(out, `class="tagchip kv clickable" data-filter="tags:epic:summariser"`) ||
		!strings.Contains(out, `<span class="k">epic</span>`) ||
		!strings.Contains(out, `<span class="v">summariser</span>`) {
		t.Errorf("kv tag should render as a clickable kv chip with k/v spans; got:\n%s", out)
	}
	if !strings.Contains(out, `class="tagchip kv cat clickable" data-filter="category:feature"`) ||
		!strings.Contains(out, `<span class="k">category</span>`) ||
		!strings.Contains(out, `<span class="v">feature</span>`) {
		t.Errorf("category should render as a distinct, clickable kv chip; got:\n%s", out)
	}
}
