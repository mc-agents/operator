package main

import (
	"flag"
	"maps"
	"slices"
	"testing"
)

// The cache is the only thing keeping a namespaced operator off every other tenant's objects: a
// DefaultNamespaces map that came out empty still starts, watches the whole cluster and is denied
// by RBAC one list at a time, far from the flag that caused it. Both spellings of the flag are
// pinned here because --namespace is what the shipped Deployments still pass.
func TestWatchNamespacesNarrowsTheCache(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "no flag leaves the cache on every namespace",
			args: nil,
		},
		{
			name: "one namespace is the one cache key",
			args: []string{"-watch-namespaces=game"},
			want: []string{"game"},
		},
		{
			name: "a comma-separated list is one key each",
			args: []string{"-watch-namespaces=game,lobby"},
			want: []string{"game", "lobby"},
		},
		{
			name: "the flag given twice keeps both",
			args: []string{"-watch-namespaces=game", "-watch-namespaces=lobby"},
			want: []string{"game", "lobby"},
		},
		{
			name: "the deprecated --namespace lands in the same map",
			args: []string{"-namespace=game"},
			want: []string{"game"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := managerOptions(parseOptions(t, tc.args)).Cache
			if got := slices.Sorted(maps.Keys(cache.DefaultNamespaces)); !slices.Equal(got, tc.want) {
				t.Fatalf("the cache is namespaced to %v, want %v", got, tc.want)
			}
		})
	}
}

func parseOptions(t *testing.T, args []string) options {
	t.Helper()
	opts := defaultOptions()
	fs := flag.NewFlagSet(componentName, flag.ContinueOnError)
	opts.bind(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return opts
}
