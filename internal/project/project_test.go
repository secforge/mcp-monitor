package project

import (
	"context"
	"testing"
)

func TestOverridePrecedence(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	cases := []struct{ own, shared, want string }{
		{a, b, a},
		{"", b, b},
	}
	for _, c := range cases {
		ResetForTesting()
		t.Setenv(EnvOverride, c.own)
		t.Setenv(EnvSharedOverride, c.shared)
		if got := Resolve(context.Background()); got != Normalize(c.want) {
			t.Fatalf("own=%q shared=%q: got %q want %q", c.own, c.shared, got, c.want)
		}
	}
	ResetForTesting()
}
