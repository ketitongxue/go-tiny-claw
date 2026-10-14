package context

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const maxSkillHeaderBytes = 16 << 10

// Skill contains discovery metadata only. Instructions are read by the model
// through the ordinary workspace tools, never eagerly added to the prompt.
type Skill struct {
	Name, Description string
	FilePath, BaseDir string
	ReadPath          string // workspace-relative canonical path for read_file
}

type SkillLoader struct{ workDir string }

func NewSkillLoader(workDir string) *SkillLoader { return &SkillLoader{workDir: workDir} }

// Discover is deterministic: lexical path order, first valid name wins. File
// symlinks are allowed only inside the workspace; directory symlinks are not
// traversed. Broken resources produce diagnostics rather than aborting startup.
func (s *SkillLoader) Discover() ([]Skill, []Diagnostic) {
	workspace, err := canonicalExisting(s.workDir)
	if err != nil {
		return nil, []Diagnostic{{"warning", s.workDir, "cannot resolve skill workspace"}}
	}
	base := filepath.Join(workspace, ".claw", "skills")
	if _, err := os.Lstat(base); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	realBase, err := canonicalExisting(base)
	if err != nil || !within(workspace, realBase) {
		return nil, []Diagnostic{{"warning", base, "skill directory must resolve inside the workspace"}}
	}
	info, err := os.Stat(realBase)
	if err != nil || !info.IsDir() {
		return nil, []Diagnostic{{"warning", base, "skills resource must be a directory"}}
	}
	var skills []Skill
	var diagnostics []Diagnostic
	names, paths := map[string]bool{}, map[string]bool{}
	warn := func(path, message string) { diagnostics = append(diagnostics, Diagnostic{"warning", path, message}) }
	err = filepath.WalkDir(realBase, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			warn(path, "cannot read skill resource")
			return nil
		}
		if d.IsDir() {
			if d.Name() == "SKILL.md" {
				warn(path, "SKILL.md must be a regular file, not a directory")
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 && d.Name() != "SKILL.md" {
			// Asset file symlinks are not discovery candidates. Directory
			// symlinks are explicitly skipped instead of following cycles.
			if target, err := os.Stat(path); err == nil && target.IsDir() {
				warn(path, "symlink skill directories are not traversed")
			}
			return nil
		}
		if d.Name() != "SKILL.md" {
			return nil
		}
		real, err := regularWithin(workspace, path)
		if err != nil {
			warn(path, "SKILL.md must be a regular file resolving inside the workspace")
			return nil
		}
		if paths[real] {
			return nil
		}
		paths[real] = true
		skill, err := readSkillMetadata(real)
		if err != nil {
			warn(path, err.Error())
			return nil
		}
		if names[skill.Name] {
			warn(path, "duplicate skill name; later resource skipped")
			return nil
		}
		names[skill.Name] = true
		skill.FilePath, skill.BaseDir = real, filepath.Dir(real)
		skill.ReadPath, _ = filepath.Rel(workspace, real)
		skills = append(skills, skill)
		return nil
	})
	if err != nil {
		warn(base, "cannot discover skills")
	}
	return skills, diagnostics
}

// Only read the bounded frontmatter. Large bodies do not affect discovery IO,
// metadata size, or system prompt size. YAML supports existing quoted/multiline
// descriptions; parser errors are not surfaced because they can quote content.
func readSkillMetadata(path string) (Skill, error) {
	f, err := os.Open(path)
	if err != nil {
		return Skill{}, errors.New("cannot read SKILL.md")
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return Skill{}, errors.New("SKILL.md must be a regular file")
	}
	scanner := bufio.NewScanner(io.LimitReader(f, maxSkillHeaderBytes+1))
	scanner.Buffer(make([]byte, 4096), maxSkillHeaderBytes+1)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return Skill{}, errors.New("SKILL.md requires YAML frontmatter")
	}
	var header strings.Builder
	closed := false
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "---" {
			closed = true
			break
		}
		header.WriteString(scanner.Text())
		header.WriteByte('\n')
	}
	if !closed || scanner.Err() != nil || header.Len() > maxSkillHeaderBytes {
		return Skill{}, errors.New("skill frontmatter missing closing delimiter or exceeds 16 KiB")
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(header.String()), &metadata); err != nil {
		return Skill{}, errors.New("invalid skill YAML frontmatter")
	}
	metadata.Name, metadata.Description = strings.TrimSpace(metadata.Name), strings.TrimSpace(metadata.Description)
	if metadata.Name == "" || utf8.RuneCountInString(metadata.Name) > 128 || strings.IndexFunc(metadata.Name, unicode.IsControl) >= 0 {
		return Skill{}, fmt.Errorf("skill name must contain 1-128 characters without control characters")
	}
	if metadata.Description == "" || utf8.RuneCountInString(metadata.Description) > 2048 {
		return Skill{}, errors.New("skill description must contain 1-2048 characters")
	}
	return Skill{Name: metadata.Name, Description: metadata.Description}, nil
}
