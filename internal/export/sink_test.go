package export

import "testing"

func TestResolveNamesAlias(t *testing.T) {
	// Snapshot and restore global registries so this test is isolated.
	origReg := registry
	origAlias := aliases
	t.Cleanup(func() { registry = origReg; aliases = origAlias })

	registry = map[string]Factory{"a": nil, "b": nil, "c": nil}
	aliases = map[string][]string{"bundle": {"a", "b"}}

	got, err := resolveNames([]string{"bundle", "c", "a"})
	if err != nil {
		t.Fatalf("resolveNames error: %v", err)
	}
	// bundle -> a,b ; then c ; then a (duplicate, dropped).
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestResolveNamesUnknown(t *testing.T) {
	origReg := registry
	origAlias := aliases
	t.Cleanup(func() { registry = origReg; aliases = origAlias })

	registry = map[string]Factory{"a": nil}
	aliases = map[string][]string{}

	if _, err := resolveNames([]string{"nope"}); err == nil {
		t.Fatal("expected error for unknown exporter")
	}
}

func TestNestedAliasExpansion(t *testing.T) {
	origReg := registry
	origAlias := aliases
	t.Cleanup(func() { registry = origReg; aliases = origAlias })

	registry = map[string]Factory{"x": nil, "y": nil, "z": nil}
	aliases = map[string][]string{
		"inner": {"x", "y"},
		"outer": {"inner", "z"},
	}

	got, err := resolveNames([]string{"outer"})
	if err != nil {
		t.Fatalf("resolveNames error: %v", err)
	}
	want := []string{"x", "y", "z"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
}
