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

func TestAssigneesOnlyRowRendersTagLine(t *testing.T) {
	now := time.Now()
	it := Item{ID: "sty_1", Kind: "story", Title: "x", Status: "open", CreatedAt: now, UpdatedAt: now}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "workitemRows", []rowVM{{Item: it, Assignees: []assigneeVM{{User: "ann", Working: true}}}}); err != nil {
		t.Fatal(err)
	}
	want := `<div class="wi-tags"><button type="button" class="tagchip kv assignee clickable" data-filter="assignee:ann" aria-label="filter by assignee:ann, working"><span class="k">assignee</span><span class="v">ann</span><span class="w">working</span></button></div>`
	if !strings.Contains(buf.String(), want) {
		t.Errorf("a row with only an assignee lacks its tag line:\n%s", buf.String())
	}
}

func TestEmptyWorkItemTablesSpanFiveColumns(t *testing.T) {
	for _, name := range []string{"storyRows", "workitemRows"} {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, name, []rowVM{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), `<td colspan="5" class="empty">`) {
			t.Errorf("%s: the empty row does not span the five columns:\n%s", name, buf.String())
		}
	}
}
