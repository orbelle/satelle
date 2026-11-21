package common

import (
	"encoding/json"
	"strings"
)

type PayloadKeys struct {
	Event     []string
	Session   []string
	Model     []string
	Tool      []string
	ToolInput []string
	Path      []string
	Command   []string
	Cwd       string
	Roots     string
	Reason    string
}

type Payload map[string]json.RawMessage

func DecodePayload(raw []byte) (Payload, bool) {
	var p Payload
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	return p, true
}

func (p Payload) Has(key string) bool { _, ok := p[key]; return ok }

func (p Payload) Str(keys ...string) string {
	for _, k := range keys {
		var s string
		if v, ok := p[k]; ok && json.Unmarshal(v, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return ""
}

func (p Payload) Sub(key string) Payload {
	var sub Payload
	if v, ok := p[key]; ok {
		_ = json.Unmarshal(v, &sub)
	}
	return sub
}

func (p Payload) Strs(key string) []string {
	var out []string
	if v, ok := p[key]; ok {
		_ = json.Unmarshal(v, &out)
	}
	return out
}

func (p Payload) Input(k PayloadKeys) HookInput {
	in := HookInput{
		Event:      p.Str(k.Event...),
		Session:    p.Str(k.Session...),
		Model:      p.Str(k.Model...),
		Tool:       p.Str(k.Tool...),
		StopReason: p.Str(k.Reason),
		Cwd:        p.Str(k.Cwd),
	}
	if k.Roots != "" {
		in.Roots = p.Strs(k.Roots)
	}
	for _, key := range k.ToolInput {
		ti := p.Sub(key)
		in.Path = FirstNonEmpty(in.Path, ti.Str(k.Path...))
		in.Command = FirstNonEmpty(in.Command, ti.Str(k.Command...))
	}
	return in
}
