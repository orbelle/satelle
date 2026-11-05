package verb_test

import (
	"github.com/bobmcallan/satelle/internal/config"
)

// sty_034d843c: controlled tag vocabulary at create/set.

func surfaceVocab() config.Config {
	return config.Config{Tags: config.TagsConfig{Vocabulary: map[string][]string{
		"surface": {"ui", "cli"},
	}}}
}
