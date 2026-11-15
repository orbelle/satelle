package kernel

import (
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/item"
)

type TagWarning struct {
	Item  string   `json:"item"`
	Tag   string   `json:"tag"`
	Named []string `json:"named"`
}

func (w TagWarning) String() string {
	return fmt.Sprintf("%s: tag %q: conventional values are %s", w.Item, w.Tag, strings.Join(w.Named, ", "))
}

func TagWarnings(items []item.Item) []TagWarning {
	out := []TagWarning{}
	for _, it := range items {
		for _, tag := range it.Tags {
			if named, outside := item.OutsideValues(tag); outside {
				out = append(out, TagWarning{Item: it.ID, Tag: tag, Named: named})
			}
		}
	}
	return out
}
