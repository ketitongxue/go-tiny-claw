package context

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func putResource(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func putSkill(t *testing.T, dir, name, description, body string) string {
	t.Helper()
	path := filepath.Join(dir, "SKILL.md")
	putResource(t, path, fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n%s", name, description, body))
	return path
}

func TestSkillDiscovery(t *testing.T) {
	workspace := t.TempDir()
	base := filepath.Join(workspace, ".claw", "skills")
	good := putSkill(t, filepath.Join(base, "a"), "go-testing", "|\n  Write Go tests\n  with tables", "BODY SECRET")
	putSkill(t, filepath.Join(base, "b"), "go-testing", "duplicate", "skip")
	putSkill(t, filepath.Join(base, "c"), "missing-description", "", "skip")
	putSkill(t, filepath.Join(base, "d"), "", "no name", "skip")
	putSkill(t, filepath.Join(base, "e"), strings.Repeat("a", 129), "too long", "skip")
	putSkill(t, filepath.Join(base, "f"), "long-description", strings.Repeat("a", 2049), "skip")
	putResource(t, filepath.Join(base, "g", "SKILL.md"), "---\nname: a\nname: b\ndescription: duplicate yaml\n---\n")
	if err := os.MkdirAll(filepath.Join(base, "h", "SKILL.md"), 0700); err != nil {
		t.Fatal(err)
	}
	skills, diagnostics := NewSkillLoader(workspace).Discover()
	if len(skills) != 1 || len(diagnostics) != 7 {
		t.Fatalf("skills=%+v diagnostics=%+v", skills, diagnostics)
	}
	real, _ := canonicalExisting(good)
	if skills[0].FilePath != real || skills[0].BaseDir != filepath.Dir(real) || skills[0].ReadPath != filepath.Join(".claw", "skills", "a", "SKILL.md") {
		t.Fatal(skills)
	}
	if skills[0].Description != "Write Go tests\nwith tables" {
		t.Fatal(skills)
	}
	again, warnings := NewSkillLoader(workspace).Discover()
	if !reflect.DeepEqual(skills, again) || !reflect.DeepEqual(diagnostics, warnings) {
		t.Fatal("non-deterministic discovery")
	}
}

func TestSkillDiscoverySymlinkBoundaryAndDedup(t *testing.T) {
	workspace := t.TempDir()
	base := filepath.Join(workspace, ".claw", "skills")
	original := putSkill(t, filepath.Join(workspace, "shared"), "shared", "internal symlink", "body")
	outside := putSkill(t, t.TempDir(), "external", "must not load", "secret")
	for _, link := range []struct{ name, target string }{{"a", original}, {"b", original}, {"c", outside}} {
		path := filepath.Join(base, link.name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(link.target, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(base, "external-dir")); err != nil {
		t.Fatal(err)
	}
	skills, diagnostics := NewSkillLoader(workspace).Discover()
	if len(skills) != 1 || len(diagnostics) != 2 || !strings.HasPrefix(skills[0].ReadPath, "shared") {
		t.Fatalf("%+v %+v", skills, diagnostics)
	}
	// References are resolved relative to the canonical SKILL.md directory.
	if filepath.Clean(filepath.Join(skills[0].BaseDir, "assets", "../check.sh")) != filepath.Join(filepath.Dir(skills[0].FilePath), "check.sh") {
		t.Fatal("wrong reference base")
	}
}

func TestSkillDiscoveryBaseDirectorySymlink(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprint(external), func(t *testing.T) {
			workspace := t.TempDir()
			target := filepath.Join(workspace, "shared-skills")
			if external {
				target = t.TempDir()
			}
			putSkill(t, filepath.Join(target, "go"), "go", "Go tests", "body")
			if err := os.MkdirAll(filepath.Join(workspace, ".claw"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(workspace, ".claw", "skills")); err != nil {
				t.Fatal(err)
			}
			skills, diagnostics := NewSkillLoader(workspace).Discover()
			if external {
				if len(skills) != 0 || len(diagnostics) != 1 {
					t.Fatal(skills, diagnostics)
				}
			} else {
				if len(skills) != 1 || len(diagnostics) != 0 {
					t.Fatal(skills, diagnostics)
				}
			}
		})
	}
}

func TestSkillLazyLoadingSize(t *testing.T) {
	workspace := t.TempDir()
	base := filepath.Join(workspace, ".claw", "skills")
	for i := 0; i < 12; i++ {
		putSkill(t, filepath.Join(base, fmt.Sprint(i)), fmt.Sprintf("skill-%02d", i), "metadata", "small")
	}
	build := func() PromptState {
		skills, diagnostics := NewSkillLoader(workspace).Discover()
		if len(skills) != 12 || len(diagnostics) != 0 {
			t.Fatal(skills, diagnostics)
		}
		state, err := NewPromptComposer().Compose(PromptInput{Skills: skills})
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	small := build()
	for i := 0; i < 12; i++ {
		putSkill(t, filepath.Join(base, fmt.Sprint(i)), fmt.Sprintf("skill-%02d", i), "metadata", strings.Repeat("LARGE-BODY-SECRET", 20000))
	}
	large := build()
	if !reflect.DeepEqual(small, large) || len(large.Content) > 10000 || strings.Contains(large.Content, "LARGE-BODY-SECRET") {
		t.Fatal("body affected prompt")
	}
	if large.SkillsCount != 12 {
		t.Fatal("skill metric missing")
	}
}

func TestSkillDiscoveryMalformedFrontmatter(t *testing.T) {
	for _, header := range []string{"no frontmatter", "---\nname: a\n", "---\nname: [invalid\ndescription: a\n---", "---\n" + strings.Repeat("#", maxSkillHeaderBytes) + "\n---"} {
		t.Run(fmt.Sprint(len(header)), func(t *testing.T) {
			workspace := t.TempDir()
			putResource(t, filepath.Join(workspace, ".claw", "skills", "bad", "SKILL.md"), header)
			skills, diagnostics := NewSkillLoader(workspace).Discover()
			if len(skills) != 0 || len(diagnostics) != 1 || diagnostics[0].Level != "warning" {
				t.Fatal(skills, diagnostics)
			}
		})
	}
}
