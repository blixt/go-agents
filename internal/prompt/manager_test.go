package prompt

import (
	"strings"
	"testing"
)

func TestMergePromptSectionsIncludesManagedContract(t *testing.T) {
	merged, err := mergePromptSections("user prompt", "managed prompt")
	if err != nil {
		t.Fatalf("mergePromptSections error: %v", err)
	}

	if !strings.Contains(merged, "user prompt") {
		t.Fatalf("expected merged prompt to include user prompt")
	}
	if !strings.Contains(merged, "Managed Harness API Context") {
		t.Fatalf("expected merged prompt to include managed section heading")
	}
	if !strings.Contains(merged, "managed prompt") {
		t.Fatalf("expected merged prompt to include managed prompt body")
	}
}

func TestMergePromptSectionsRejectsEmpty(t *testing.T) {
	if _, err := mergePromptSections("", "   "); err == nil {
		t.Fatalf("expected error when all prompt sections are empty")
	}
}
