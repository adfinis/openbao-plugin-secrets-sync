package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

const (
	// docsIndex is the only page exempt from verifiedBy, and it must link to every other page.
	docsIndex  = "docs/README.md"
	rootReadme = "README.md"
)

var (
	linkPattern    = regexp.MustCompile(`!?\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	headingPattern = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	fencePattern   = regexp.MustCompile("^\\s*(```|~~~)")
)

type frontmatter struct {
	VerifiedBy []string `yaml:"verifiedBy"`
}

type page struct {
	rel      string
	links    []link
	anchors  map[string]bool
	verified []string
	err      error
}

type link struct {
	line   int
	target string
}

func main() {
	if err := checkDocs("."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Docs have verifiedBy evidence, a complete index, and resolvable links.")
}

func checkDocs(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	files, err := markdownFiles(root)
	if err != nil {
		return err
	}
	pages := make(map[string]*page, len(files))
	for _, rel := range files {
		pages[rel] = parsePage(root, rel)
	}
	var errs []error
	for _, rel := range files {
		p := pages[rel]
		if p.err != nil {
			errs = append(errs, p.err)
			continue
		}
		errs = append(errs, checkVerifiedBy(root, p), checkLinks(root, p, pages))
	}
	errs = append(errs, checkIndex(pages))
	return errors.Join(errs...)
}

// markdownFiles returns the docs tree plus the repository pages that link into it.
func markdownFiles(root string) ([]string, error) {
	var files []string
	for _, rel := range []string{rootReadme, "CONTRIBUTING.md", "SECURITY.md"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			files = append(files, rel)
		}
	}
	for _, dir := range []string{"docs", "release-notes", "test/e2e"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, fs.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".md" {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(files)
	return files, nil
}

func parsePage(root, rel string) *page {
	p := &page{rel: rel, anchors: map[string]bool{}}
	text, err := readText(filepath.Join(root, rel))
	if err != nil {
		p.err = err
		return p
	}
	body, offset, verified, err := splitFrontmatter(text)
	if err != nil {
		p.err = fmt.Errorf("%s: %w", rel, err)
		return p
	}
	p.verified = verified
	seen := map[string]int{}
	inFence := false
	for i, line := range strings.Split(body, "\n") {
		if fencePattern.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := headingPattern.FindStringSubmatch(line); m != nil {
			slug := headingSlug(m[2])
			if n := seen[slug]; n > 0 {
				p.anchors[fmt.Sprintf("%s-%d", slug, n)] = true
			} else {
				p.anchors[slug] = true
			}
			seen[slug]++
		}
		for _, m := range linkPattern.FindAllStringSubmatch(stripInlineCode(line), -1) {
			p.links = append(p.links, link{line: offset + i + 1, target: m[1]})
		}
	}
	return p
}

func splitFrontmatter(text string) (body string, offset int, verified []string, err error) {
	if !strings.HasPrefix(text, "---\n") {
		return text, 0, nil, nil
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return "", 0, nil, errors.New("unterminated frontmatter")
	}
	raw := text[4 : 4+end]
	var fm frontmatter
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&fm); err != nil {
		return "", 0, nil, fmt.Errorf("frontmatter: %w", err)
	}
	return text[4+end+5:], strings.Count(raw, "\n") + 3, fm.VerifiedBy, nil
}

func checkVerifiedBy(root string, p *page) error {
	if !strings.HasPrefix(p.rel, "docs/") {
		return nil
	}
	if p.rel == docsIndex {
		if len(p.verified) > 0 {
			return fmt.Errorf("%s: the index page must not declare verifiedBy", p.rel)
		}
		return nil
	}
	if len(p.verified) == 0 {
		return fmt.Errorf("%s: frontmatter must list verifiedBy evidence", p.rel)
	}
	var errs []error
	for _, evidence := range p.verified {
		clean := filepath.ToSlash(filepath.Clean(evidence))
		if clean != evidence || filepath.IsAbs(evidence) || strings.HasPrefix(clean, "../") {
			errs = append(errs, fmt.Errorf("%s: verifiedBy path %q must be repository-relative and clean", p.rel, evidence))
			continue
		}
		if _, err := os.Stat(filepath.Join(root, clean)); err != nil {
			errs = append(errs, fmt.Errorf("%s: verifiedBy path %q does not exist", p.rel, evidence))
		}
	}
	return errors.Join(errs...)
}

func checkLinks(root string, p *page, pages map[string]*page) error {
	var errs []error
	for _, l := range p.links {
		if isExternal(l.target) {
			continue
		}
		pathPart, anchor, _ := strings.Cut(l.target, "#")
		targetRel := p.rel
		if pathPart != "" {
			targetRel = filepath.ToSlash(filepath.Join(filepath.Dir(p.rel), pathPart))
			if strings.HasPrefix(targetRel, "../") {
				errs = append(errs, fmt.Errorf("%s:%d: link %q leaves the repository", p.rel, l.line, l.target))
				continue
			}
			if _, err := os.Stat(filepath.Join(root, targetRel)); err != nil {
				errs = append(errs, fmt.Errorf("%s:%d: link target %q does not exist", p.rel, l.line, l.target))
				continue
			}
		}
		if anchor == "" || filepath.Ext(targetRel) != ".md" {
			continue
		}
		target, ok := pages[targetRel]
		if !ok {
			target = parsePage(root, targetRel)
		}
		if target.err == nil && !target.anchors[anchor] {
			errs = append(errs, fmt.Errorf("%s:%d: anchor %q not found in %s", p.rel, l.line, anchor, targetRel))
		}
	}
	return errors.Join(errs...)
}

func checkIndex(pages map[string]*page) error {
	index, ok := pages[docsIndex]
	if !ok {
		return fmt.Errorf("%s is missing", docsIndex)
	}
	linked := map[string]bool{}
	for _, l := range index.links {
		if pathPart, _, _ := strings.Cut(l.target, "#"); pathPart != "" && !isExternal(l.target) {
			linked[filepath.ToSlash(filepath.Join(filepath.Dir(docsIndex), pathPart))] = true
		}
	}
	var errs []error
	for rel := range pages {
		if strings.HasPrefix(rel, "docs/") && rel != docsIndex && !linked[rel] {
			errs = append(errs, fmt.Errorf("%s: page is not linked from %s", rel, docsIndex))
		}
	}
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return errors.Join(errs...)
}

func isExternal(target string) bool {
	return strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:")
}

// headingSlug follows GitHub's anchor generation: lowercase, drop punctuation, and turn spaces into hyphens.
func headingSlug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func stripInlineCode(line string) string {
	parts := strings.Split(line, "`")
	for i := 1; i < len(parts); i += 2 {
		parts[i] = ""
	}
	return strings.Join(parts, "`")
}

func readText(path string) (string, error) {
	// #nosec G304 -- the checker reads repository documentation under its caller's workspace root.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
