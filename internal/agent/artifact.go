package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/bobmcallan/satelle/internal/item"
)

// Artifact is a performer's structured result: a document it hands back for
// satelle to attach to the item, named by the artifact.
type Artifact struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Body string `json:"body"`
}

// MediaType is the attachment media type: the body is markdown, and the
// artifact's type travels as a parameter (text/markdown; type=plan).
func (a Artifact) MediaType() string {
	return item.TypedMediaType("text/markdown", a.Type)
}

// ValidType reports whether typ is a valid attachment type: a plain token, so
// the media-type parameter needs no quoting.
func ValidType(typ string) bool { return artifactType.MatchString(typ) }

// artifactKey spots an "artifact" key written raw in the text, which a valid
// object would have decoded; an escaped mention inside a JSON string does not
// match.
var artifactKey = regexp.MustCompile(`(^|[^\\])"artifact"\s*:`)

// artifactType is a plain token, so the media-type parameter needs no quoting.
var artifactType = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ParseArtifact finds the final {"artifact":{name,type,body}} object in a
// performer's answer. No artifact is (nil, nil). An artifact object that is
// incomplete, or an "artifact" key that is not valid JSON, is an error: a
// performer that meant to hand back a document must not pass silently.
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
