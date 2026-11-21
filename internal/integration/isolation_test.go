package integration

import (
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/bobmcallan/satelle/internal/"

var providerPackages = []string{
	module + "integration/claude",
	module + "integration/cursor",
	module + "integration/grok",
	module + "integration/pi",
}

func imports(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", pkg, err, out)
	}
	return strings.Fields(string(out))
}

func refuse(t *testing.T, pkg string, forbidden []string) {
	t.Helper()
	for _, imp := range imports(t, pkg) {
		for _, f := range forbidden {
			if imp == f || strings.HasPrefix(imp, f+"/") {
				t.Errorf("%s imports %s", pkg, imp)
			}
		}
	}
}

func TestProvidersImportNoOtherProvider(t *testing.T) {
	for _, p := range providerPackages {
		var others []string
		for _, o := range providerPackages {
			if o != p {
				others = append(others, o)
			}
		}
		refuse(t, p, append(others, module+"kernel", module+"cli", module+"hook", module+"agent"))
	}
}

func TestCommonImportsNoProviderOrKernel(t *testing.T) {
	refuse(t, module+"integration/common", append([]string{module + "kernel", module + "cli", module + "hook", module + "agent", module + "integration"}, providerPackages...))
	refuse(t, module+"integration", []string{module + "kernel", module + "cli", module + "hook", module + "agent"})
}

func TestFacadesReachProvidersOnlyThroughTheRegistry(t *testing.T) {
	for _, facade := range []string{module + "hook", module + "agent"} {
		refuse(t, facade, providerPackages)
	}
}
