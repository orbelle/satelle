package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"regexp"
	"strings"
)

type Artifact struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Body string `json:"body"`
}

func (a Artifact) MediaType() string {
	return TypedMediaType("text/markdown", a.Type)
}

func TypedMediaType(mt, typ string) string {
	base, params, err := mime.ParseMediaType(mt)
	if err != nil {
		return ""
	}
	params["type"] = typ
	return mime.FormatMediaType(base, params)
}

func MediaTypeParam(mt, key string) string {
	_, params, err := mime.ParseMediaType(mt)
	if err != nil {
		return ""
	}
	return params[key]
}

func ValidType(typ string) bool { return artifactType.MatchString(typ) }

var artifactKey = regexp.MustCompile(`(^|[^\\])"artifact"\s*:`)

var artifactType = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func ParseArtifact(text string) (*Artifact, error) {
	raw, ok := LastObject(text, func(r json.RawMessage) bool {
		var probe map[string]json.RawMessage
		if json.Unmarshal(r, &probe) != nil {
			return false
		}
		_, has := probe["artifact"]
		return has
	})
	if !ok {
		if artifactKey.MatchString(text) {
			return nil, errors.New("malformed artifact: an artifact object in the output is not valid JSON")
		}
		return nil, nil
	}
	var outer struct {
		Artifact json.RawMessage `json:"artifact"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("malformed artifact: %v", err)
	}
	var a Artifact
	if err := json.Unmarshal(outer.Artifact, &a); err != nil {
		return nil, fmt.Errorf("malformed artifact: artifact must be an object with string name, type and body: %v", err)
	}
	for field, v := range map[string]string{"name": a.Name, "type": a.Type, "body": a.Body} {
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("malformed artifact: %s is missing or blank", field)
		}
	}
	if !ValidType(a.Type) || a.MediaType() == "" {
		return nil, fmt.Errorf("malformed artifact: type %q is not a valid media-type parameter value (letters, digits, '.', '_', '-')", a.Type)
	}
	return &a, nil
}
