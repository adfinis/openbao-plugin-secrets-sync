package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

type imagePolicy struct {
	Primary     string `yaml:"primary"`
	Image       string `yaml:"image"`
	ImageDigest string `yaml:"imageDigest"`
}

type versionPolicy struct {
	Toolchain struct {
		Go            string            `yaml:"go"`
		QualityTools  map[string]string `yaml:"qualityTools"`
		GitHubActions map[string]string `yaml:"githubActions"`
	} `yaml:"toolchain"`
	Validation struct {
		OpenBao    imagePolicy `yaml:"openbao"`
		GitLab     imagePolicy `yaml:"gitlab"`
		Kubernetes struct {
			Kind      string `yaml:"kind"`
			Kubectl   string `yaml:"kubectl"`
			NodeImage string `yaml:"nodeImage"`
		} `yaml:"kubernetes"`
	} `yaml:"validation"`
}

func main() {
	if err := checkVersionPolicy("."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Version policy matches toolchain, workflow pins, and fixture defaults.")
}

func checkVersionPolicy(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	node, err := readYAML(filepath.Join(root, ".ci/versions.yaml"))
	if err != nil {
		return err
	}
	var p versionPolicy
	if err := node.Decode(&p); err != nil {
		return fmt.Errorf("decode version policy: %w", err)
	}
	if err := validatePolicy(p); err != nil {
		return err
	}
	return errors.Join(checkDefaults(root, p), checkCompose(root, p), checkWorkflows(root, p))
}

func validatePolicy(p versionPolicy) error {
	version := regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	if !version.MatchString(p.Toolchain.Go) {
		return errors.New("toolchain.go must declare an exact Go patch version")
	}
	for repo, pin := range p.Toolchain.GitHubActions {
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(pin) {
			return fmt.Errorf("githubActions.%s must be a full commit ID", repo)
		}
	}
	for tool, value := range p.Toolchain.QualityTools {
		if tool != "pinStatus" && !version.MatchString(value) {
			return fmt.Errorf("qualityTools.%s must declare an exact patch version", tool)
		}
	}
	return errors.Join(
		validateImagePolicy("ghcr.io/openbao/openbao", "", p.Validation.OpenBao),
		validateImagePolicy("gitlab/gitlab-ce", "-ce.0", p.Validation.GitLab),
	)
}

func validateImagePolicy(repo, suffix string, image imagePolicy) error {
	expected := repo + ":" + image.Primary + suffix + "@" + image.ImageDigest
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(image.Primary) || image.Image != expected ||
		!regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image.ImageDigest) {
		return fmt.Errorf("%s policy must contain a matching version, image, and SHA-256 digest", repo)
	}
	return nil
}

func checkDefaults(root string, p versionPolicy) error {
	type declaration struct{ path, prefix, value string }
	checks := make([]declaration, 0, 12)
	checks = append(checks, []declaration{
		{".go-version", "", p.Toolchain.Go},
		{"go.mod", "go ", p.Toolchain.Go},
		{"mk/e2e.mk", "E2E_OPENBAO_IMAGE ?= ", p.Validation.OpenBao.Image},
		{"mk/e2e.mk", "E2E_GITLAB_IMAGE ?= ", p.Validation.GitLab.Image},
		{"mk/e2e.mk", "E2E_KIND_NODE_IMAGE ?= ", p.Validation.Kubernetes.NodeImage},
		{"test/e2e/kind/Dockerfile", "ARG OPENBAO_IMAGE=", p.Validation.OpenBao.Image},
	}...)
	for tool, variable := range map[string]string{
		"actionlint": "ACTIONLINT_VERSION", "gofumpt": "GOFUMPT_VERSION", "staticcheck": "STATICCHECK_VERSION",
		"govulncheck": "GOVULNCHECK_VERSION", "golangciLint": "GOLANGCI_LINT_VERSION", "goLicenses": "GO_LICENSES_VERSION",
	} {
		checks = append(checks, declaration{
			"mk/config.mk", variable + " ?= ", "v" + p.Toolchain.QualityTools[tool],
		})
	}
	var problems []error
	for _, check := range checks {
		text, err := readText(filepath.Join(root, check.path))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		found := false
		for line := range strings.SplitSeq(text, "\n") {
			if !strings.HasPrefix(line, check.prefix) {
				continue
			}
			found = true
			actual, _, _ := strings.Cut(strings.TrimPrefix(line, check.prefix), "#")
			if strings.TrimSpace(actual) != check.value || check.value == "" {
				problems = append(problems, fmt.Errorf("%s: %s must equal %s", check.path, check.prefix, check.value))
			}
			break
		}
		if !found {
			problems = append(problems, fmt.Errorf("%s: missing version declaration %s", check.path, check.prefix))
		}
	}
	return errors.Join(problems...)
}

func findComposeFiles(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(filepath.Join(root, "test/e2e"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && (entry.Name() == "compose.yaml" || entry.Name() == "compose.yml") {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

func checkCompose(root string, p versionPolicy) error {
	paths, err := findComposeFiles(root)
	if err != nil {
		return err
	}
	var problems []error
	baoCount, gitlabCount := 0, 0
	for _, path := range paths {
		node, err := readYAML(path)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		var compose struct {
			Services map[string]struct{ Image string }
		}
		if err := node.Decode(&compose); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", path, err))
			continue
		}
		for name, service := range compose.Services {
			expected := ""
			switch {
			case name == "gitlab" || referencesImage(service.Image,
				"gitlab/gitlab-ce", "docker.io/gitlab/gitlab-ce", "index.docker.io/gitlab/gitlab-ce"):
				expected = "${E2E_GITLAB_IMAGE:-" + p.Validation.GitLab.Image + "}"
				gitlabCount++
			case strings.HasPrefix(name, "openbao") || name == "plugin-dir" ||
				referencesImage(service.Image, "ghcr.io/openbao/openbao", "openbao/openbao",
					"docker.io/openbao/openbao", "index.docker.io/openbao/openbao"):
				expected = "${E2E_OPENBAO_IMAGE:-" + p.Validation.OpenBao.Image + "}"
				baoCount++
			default:
				continue
			}
			if service.Image != expected {
				problems = append(problems, fmt.Errorf("%s: services.%s.image must equal %s", path, name, expected))
			}
		}
	}
	if baoCount == 0 || gitlabCount == 0 {
		problems = append(problems, errors.New("version policy requires OpenBao and GitLab Compose fixtures"))
	}
	return errors.Join(problems...)
}

func referencesImage(image string, repositories ...string) bool {
	if strings.HasPrefix(image, "${") && strings.HasSuffix(image, "}") {
		expansion := strings.TrimSuffix(strings.TrimPrefix(image, "${"), "}")
		_, fallback, ok := strings.Cut(expansion, ":-")
		if !ok {
			_, fallback, ok = strings.Cut(expansion, "-")
		}
		if !ok {
			return false
		}
		image = fallback
	}
	for _, repository := range repositories {
		if image == repository || strings.HasPrefix(image, repository+":") || strings.HasPrefix(image, repository+"@") {
			return true
		}
	}
	return false
}

func checkWorkflows(root string, p versionPolicy) error {
	var problems []error
	err := filepath.WalkDir(filepath.Join(root, ".github"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || (filepath.Ext(path) != ".yml" && filepath.Ext(path) != ".yaml") {
			return nil
		}
		node, err := readYAML(path)
		if err != nil {
			return err
		}
		walkMappings(node, func(mapping *yaml.Node) {
			if mappingValue(mapping, "uses") != nil {
				uses := scalar(mapping, "uses")
				if uses == "" {
					problems = append(problems, fmt.Errorf("%s: uses must resolve to a literal action reference", path))
				} else {
					problems = append(problems, checkAction(root, path, mapping, uses, p))
				}
			}
			for key, expected := range map[string]string{
				"SEMGREP_VERSION": p.Toolchain.QualityTools["semgrep"],
				"KIND_VERSION":    p.Validation.Kubernetes.Kind,
				"KUBECTL_VERSION": p.Validation.Kubernetes.Kubectl,
			} {
				if actual := scalar(mapping, key); mappingValue(mapping, key) != nil && actual != expected {
					problems = append(problems, fmt.Errorf("%s: %s must equal %s", path, key, expected))
				}
			}
		})
		return nil
	})
	return errors.Join(append(problems, err)...)
}

func checkAction(root, path string, node *yaml.Node, uses string, p versionPolicy) error {
	if strings.HasPrefix(uses, "./") {
		resolved, err := filepath.EvalSymlinks(filepath.Join(root, uses))
		if err != nil {
			return err
		}
		local, err := filepath.Rel(root, resolved)
		if err != nil {
			return err
		}
		local = filepath.ToSlash(local)
		if !strings.HasPrefix(local, ".github/actions/") && !strings.HasPrefix(local, ".github/workflows/") {
			return fmt.Errorf("%s: keep local actions and workflows in .github for pin discovery: %s", path, uses)
		}
		return nil
	}
	repo, pin, ok := strings.Cut(uses, "@")
	expected := p.Toolchain.GitHubActions[strings.ToLower(repo)]
	if !ok || expected == "" || pin != expected {
		return fmt.Errorf("%s: %s must use its reviewed githubActions policy pin", path, uses)
	}
	option, tool := "version", ""
	switch strings.ToLower(repo) {
	case "aquasecurity/setup-trivy":
		tool = "trivy"
	case "docker/setup-buildx-action":
		tool = "buildx"
	case "sigstore/cosign-installer":
		option, tool = "cosign-release", "cosign"
	default:
		return nil
	}
	expected = "v" + p.Toolchain.QualityTools[tool]
	if actual := scalar(mappingValue(node, "with"), option); actual != expected {
		return fmt.Errorf("%s: %s %s must equal %s", path, repo, option, expected)
	}
	return nil
}

func walkMappings(node *yaml.Node, visit func(*yaml.Node)) {
	if node.Kind == yaml.MappingNode {
		visit(node)
	}
	for _, child := range node.Content {
		walkMappings(child, visit)
	}
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node != nil && node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return node.Content[i+1]
			}
		}
	}
	return nil
}

func scalar(node *yaml.Node, key string) string {
	value := mappingValue(node, key)
	if value != nil && value.Kind == yaml.AliasNode {
		value = value.Alias
	}
	if value != nil && value.Kind == yaml.ScalarNode {
		return value.Value
	}
	return ""
}

func readYAML(path string) (*yaml.Node, error) {
	text, err := readText(path)
	if err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(text), &node); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := uniqueMappingKeys(&node); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &node, nil
}

func uniqueMappingKeys(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode {
				return errors.New("mapping keys must be literal scalars")
			}
			if seen[key.Value] {
				return fmt.Errorf("duplicate mapping key %s at line %d", key.Value, key.Line)
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := uniqueMappingKeys(child); err != nil {
			return err
		}
	}
	return nil
}

func readText(path string) (string, error) {
	// #nosec G304 -- the checker reads repository configuration under its caller's workspace root.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
