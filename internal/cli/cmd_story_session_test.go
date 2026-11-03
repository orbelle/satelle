package cli

// `satelle story session` (sty_9914df10 AC3, AC4): machine A runs agent steps
// under a configuration, pushes; machine B holds nothing but what a pull
// brought, and still reads the session and rebuilds the configuration.

import ()

const sessionSkillBody = "# Session probe\n\n### A heading inside a body\n\nrule one — café ✓ <b>&</b>\n"

// sessionToml is the two-area push config plus a [substrate_roots] override, so
// the skills kind lives outside the default data dir on machine A.
const sessionToml = ledgerPushToml + "\n[substrate_roots]\nskills = \"alt\"\n"

type sessionConfigJSON struct {
	Bundle              string `json:"bundle"`
	SubstrateDigest     string `json:"substrate_digest"`
	BindingsDigest      string `json:"bindings_digest"`
	RecomputedSubstrate string `json:"recomputed_substrate_digest"`
	RecomputedBindings  string `json:"recomputed_bindings_digest"`
	Verified            bool   `json:"verified"`
	Files               []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"files"`
}
