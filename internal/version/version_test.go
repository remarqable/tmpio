package version

import (
	"strings"
	"testing"
)

func TestStringPrefersLinkerValues(t *testing.T) {
	old, oldD := Version, Date
	t.Cleanup(func() { Version, Date = old, oldD })

	Version, Date = "v1.3.0", "2026-09-20"
	if got := String(); got != "v1.3.0 · 2026-09-20" {
		t.Fatalf("got %q", got)
	}

	// A release with no date still names itself rather than showing a stray
	// separator.
	Date = ""
	if got := String(); got != "v1.3.0" {
		t.Fatalf("got %q", got)
	}
}

// TestUnstampedBuildSaysSomethingTrue covers the developer's binary: no
// linker flags, so it must not claim a version it does not have.
func TestUnstampedBuildSaysSomethingTrue(t *testing.T) {
	old, oldD := Version, Date
	t.Cleanup(func() { Version, Date = old, oldD })

	Version, Date = "", ""
	v, _ := Info()
	if v == "" {
		t.Fatal("an unstamped build must still report something")
	}
	if strings.HasPrefix(v, "v") {
		t.Fatalf("an unstamped build must not look like a release tag, got %q", v)
	}
}
