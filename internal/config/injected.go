package config

// InstructionReviewConfig is the [instruction_review] table:
//
//	[instruction_review]
//	extra_paths = ["internal/config/substrate/skills/**"]
//
// The shipped trigger skill (satelle-instruction-change-trigger) counts changes
// to .satelle/{skills,principles} and the constitution; a repo that keeps
// injected instruction text elsewhere — embedded defaults in its own tree — names
// the globs here. The trigger's shell script reads the key from the file, so no
// Go code consumes it; the field documents the schema and keeps the key a known
// one. The binary ships no path of its own.
type InstructionReviewConfig struct {
	ExtraPaths []string `toml:"extra_paths"`
}

// ValidateConfig is the [validate] table.
type ValidateConfig struct {
	Injected InjectedConfig `toml:"injected"`
}

// InjectedConfig is the [validate.injected] table. It configures nothing now:
// its budget sub-table, which once fitted each seat's injected principles to a
// size, is removed (sty_58a7bdfe) and refused at load until satelle init removes
// it. `satelle validate` still reports each seat's measured bytes.
type InjectedConfig struct {
	Budget removedInjectedBudget `toml:"budget"`
}
