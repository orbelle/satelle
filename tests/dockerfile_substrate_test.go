//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var requiredNodePackages = []string{"git", "jq", "bash"}

func finalStagePackages(dockerfile string) []string {
	var lines []string
	var cur strings.Builder
	for _, raw := range strings.Split(dockerfile, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, `\`) {
			cur.WriteString(strings.TrimSuffix(line, `\`))
			cur.WriteString(" ")
			continue
		}
		cur.WriteString(line)
		lines = append(lines, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}

	var stage []string
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if strings.EqualFold(f[0], "FROM") {
			stage = nil
			continue
		}
		stage = append(stage, line)
	}

	var pkgs []string
	for _, line := range stage {
		f := strings.Fields(line)
		if !strings.EqualFold(f[0], "RUN") {
			continue
		}
		for i := 1; i+1 < len(f); i++ {
			if f[i] != "apk" || f[i+1] != "add" {
				continue
			}
			for _, w := range f[i+2:] {
				if w == "&&" || w == ";" || w == "|" || w == "||" {
					break
				}
				if strings.HasPrefix(w, "-") {
					continue
				}
				pkgs = append(pkgs, strings.TrimSuffix(w, ";"))
				if strings.HasSuffix(w, ";") {
					break
				}
			}
		}
	}
	return pkgs
}

func missingPackages(pkgs, required []string) []string {
	have := map[string]bool{}
	for _, p := range pkgs {
		have[p] = true
	}
	var missing []string
	for _, r := range required {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

func TestNodeImageFinalStageInstallsTheNodeTools(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	pkgs := finalStagePackages(string(b))
	for _, m := range missingPackages(pkgs, requiredNodePackages) {
		t.Errorf("the node image's final stage does not install %s (packages: %v)", m, pkgs)
	}
}

func TestFinalStagePackagesReadsOnlyTheLastStage(t *testing.T) {
	cases := []struct {
		name       string
		dockerfile string
		required   []string
		missing    []string
	}{
		{
			name: "jq in the final stage",
			dockerfile: `FROM golang:1.27-alpine AS build
RUN apk add --no-cache make bash
FROM alpine:3
RUN apk add --no-cache git ca-certificates jq
COPY --from=build /src/satelle /usr/local/bin/satelle
`,
		},
		{
			name: "final stage lacks jq",
			dockerfile: `FROM golang:1.27-alpine AS build
RUN apk add --no-cache make bash
FROM alpine:3
RUN apk add --no-cache git ca-certificates
`,
			missing: []string{"jq"},
		},
		{
			name: "jq only in the build stage",
			dockerfile: `FROM golang:1.27-alpine AS build
RUN apk add --no-cache make bash jq
FROM alpine:3
RUN apk add --no-cache git ca-certificates
`,
			missing: []string{"jq"},
		},
		{
			name: "a later FROM replaces the stage that installs jq",
			dockerfile: `FROM golang:1.27-alpine AS build
RUN apk add --no-cache make bash
FROM alpine:3
RUN apk add --no-cache git ca-certificates jq
FROM alpine:3
RUN apk add --no-cache git ca-certificates
`,
			missing: []string{"jq"},
		},
		{
			name: "bash only in the build stage",
			dockerfile: `FROM golang:1.27-alpine AS build
RUN apk add --no-cache make bash
FROM alpine:3
RUN apk add --no-cache git ca-certificates jq
`,
			required: []string{"bash"},
			missing:  []string{"bash"},
		},
		{
			name: "apk add split across a continuation",
			dockerfile: `FROM alpine:3
# packages
RUN apk add --no-cache git \
    ca-certificates jq && echo done
`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			required := c.required
			if required == nil {
				required = []string{"jq"}
			}
			got := missingPackages(finalStagePackages(c.dockerfile), required)
			if !reflect.DeepEqual(got, c.missing) {
				t.Errorf("missing packages = %v, want %v", got, c.missing)
			}
		})
	}
}
