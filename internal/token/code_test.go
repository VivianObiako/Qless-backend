package token_test

import (
	"strings"
	"testing"

	"github.com/vivianobiako/qless/api/internal/token"
)

// Normalising is what lets someone type a code the way they read it. It is also
// the one place where being too eager would be a disaster: a rule that
// collapsed everything to the same string would make every code match.
func TestNormalizeCodeAcceptsHowAPersonTypesIt(t *testing.T) {
	code, err := token.NewCode()
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}

	canonical := token.NormalizeCode(code)
	if canonical == "" {
		t.Fatal("a freshly generated code normalised to nothing")
	}

	variants := map[string]string{
		"as issued":     code,
		"lower case":    strings.ToLower(code),
		"spaced":        strings.ReplaceAll(code, "-", " "),
		"run together":  strings.ReplaceAll(code, "-", ""),
		"padded":        "  " + code + "\n",
		"spaced dashes": strings.ReplaceAll(code, "-", " - "),
	}

	for name, variant := range variants {
		if got := token.NormalizeCode(variant); got != canonical {
			t.Errorf("%s: normalised to %q, want %q", name, got, canonical)
		}
	}
}

// The alphabet drops the characters people confuse, and normalising folds the
// confusable ones back in rather than rejecting the code.
func TestNormalizeCodeFoldsConfusableCharacters(t *testing.T) {
	if got := token.NormalizeCode("O0-Il1"); got != "00111" {
		t.Errorf("normalised to %q, want the O folded to zero and I and L folded to one", got)
	}
}

func TestDistinctCodesDoNotCollide(t *testing.T) {
	seen := map[string]bool{}

	for i := 0; i < 100; i++ {
		code, err := token.NewCode()
		if err != nil {
			t.Fatalf("generate code: %v", err)
		}

		hash := token.HashCode(code)
		if seen[hash] {
			t.Fatalf("generated the same code twice: %s", code)
		}
		seen[hash] = true
	}
}

// The prefix is a rate limiting key, so it has to be stable across the same
// spacings the lookup is.
func TestCodePrefixIsStableAcrossSpacing(t *testing.T) {
	spaced := token.CodePrefix("ab cd-ef gh")
	joined := token.CodePrefix("ABCDEFGH")

	if spaced != joined {
		t.Errorf("prefix of a spaced code = %q, of the same code joined = %q", spaced, joined)
	}
	if len(joined) != 4 {
		t.Errorf("prefix = %q, want the first group of four", joined)
	}
}
