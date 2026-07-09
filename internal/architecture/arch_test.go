package architecture

import (
	"os/exec"
	"strings"
	"testing"
)

// module is the Go module path; internal packages hang off it.
const module = "github-release-notifier"

// goList runs `go list <args...>` and returns the whitespace-separated output.
// We shell out to the toolchain (rather than pull in golang.org/x/tools) so the
// tests stay dependency-free and read exactly what `go build` would.
func goList(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		t.Fatalf("go list %v: %v", args, err)
	}
	return strings.Fields(string(out))
}

// transitiveDeps returns every package pkg imports, directly or transitively
// (the set `go build` would compile in).
func transitiveDeps(t *testing.T, pkg string) map[string]bool {
	set := map[string]bool{}
	for _, p := range goList(t, "-deps", pkg) {
		set[p] = true
	}
	return set
}

// directImports returns only the packages pkg imports directly (one hop).
func directImports(t *testing.T, pkg string) map[string]bool {
	set := map[string]bool{}
	for _, p := range goList(t, "-f", `{{join .Imports "\n"}}`, pkg) {
		set[p] = true
	}
	return set
}

// domainPkgs are the business-logic packages. They sit "inside" the transport
// layer and must not depend on it.
var domainPkgs = []string{
	module + "/internal/subscription",
	module + "/internal/releasetracking",
	module + "/internal/orchestrator",
}

// TestArch_MonolithHasNoSMTP: SMTP is the notifier service's private concern
// (HW7). The monolith must never pull net/smtp — even transitively.
func TestArch_MonolithHasNoSMTP(t *testing.T) {
	if transitiveDeps(t, module)["net/smtp"] {
		t.Error("the monolith imports net/smtp — SMTP must live only in cmd/notifier")
	}
}

// TestArch_DomainDoesNotImportTransport: dependencies point inward. The domain
// must not depend on the delivery layer (handler / app / middleware).
func TestArch_DomainDoesNotImportTransport(t *testing.T) {
	transport := []string{
		module + "/internal/handler",
		module + "/internal/app",
		module + "/internal/middleware",
	}
	for _, d := range domainPkgs {
		deps := transitiveDeps(t, d)
		for _, tr := range transport {
			if deps[tr] {
				t.Errorf("%s depends on transport %s — the domain must not know about the delivery layer", d, tr)
			}
		}
	}
}

// TestArch_DomainIsTransportNeutral: the domain must not reach for net/http
// directly — HTTP status mapping lives in the handler (translateError).
func TestArch_DomainIsTransportNeutral(t *testing.T) {
	for _, d := range domainPkgs {
		if directImports(t, d)["net/http"] {
			t.Errorf("%s imports net/http directly — keep the domain transport-neutral", d)
		}
	}
}

// TestArch_SharedKernelIsLeaf: repospec is the shared kernel (a value object).
// It must depend on nothing internal, so any layer can use it freely.
func TestArch_SharedKernelIsLeaf(t *testing.T) {
	for imp := range directImports(t, module+"/internal/repospec") {
		if strings.HasPrefix(imp, module+"/internal/") {
			t.Errorf("repospec imports %s — the shared kernel must depend on nothing internal", imp)
		}
	}
}

// TestArch_HandlerGoesThroughService: the transport layer talks to the service,
// not to infrastructure adapters directly.
func TestArch_HandlerGoesThroughService(t *testing.T) {
	infra := []string{
		module + "/internal/githubgateway",
		module + "/internal/outbox",
		module + "/internal/notification",
		module + "/internal/cache",
	}
	imps := directImports(t, module+"/internal/handler")
	for _, in := range infra {
		if imps[in] {
			t.Errorf("handler imports infra %s directly — go through subscription.Service", in)
		}
	}
}
