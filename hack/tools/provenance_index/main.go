package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type config struct {
	indexPath                   string
	repo                        string
	owner                       string
	version                     string
	pluginVersion               string
	sourceDateEpoch             int64
	binaryName                  string
	releaseSourceRef            string
	releaseWorkflow             string
	checksumsPath               string
	checksumsBundlePath         string
	sbomGlob                    string
	reproducibilityReportPath   string
	reproducible                bool
	attestationsAvailable       bool
	attestationsUnavailableNote string
}

type provenanceIndex struct {
	SchemaVersion  int                 `json:"schema_version"`
	Project        projectInfo         `json:"project"`
	Release        releaseInfo         `json:"release"`
	Checksums      checksumsInfo       `json:"checksums"`
	Assets         []assetInfo         `json:"assets"`
	SBOMs          []evidenceFile      `json:"sboms"`
	Reproducible   reproducibilityInfo `json:"reproducibility"`
	Attestations   attestationInfo     `json:"attestations"`
	OCIPluginImage ociPluginImageInfo  `json:"oci_plugin_image"`
}

type projectInfo struct {
	Repository string `json:"repository"`
	Owner      string `json:"owner"`
	Binary     string `json:"binary"`
}

type releaseInfo struct {
	Version            string `json:"version"`
	PluginVersion      string `json:"plugin_version"`
	SourceRef          string `json:"source_ref"`
	SourceDateEpoch    int64  `json:"source_date_epoch"`
	GeneratedAtUTC     string `json:"generated_at_utc"`
	ReleaseWorkflow    string `json:"release_workflow"`
	ExpectedOIDCIssuer string `json:"expected_oidc_issuer"`
}

type checksumsInfo struct {
	Path                  string `json:"path"`
	Digest                string `json:"digest"`
	SignatureBundlePath   string `json:"signature_bundle_path"`
	SignatureBundleDigest string `json:"signature_bundle_digest"`
}

type assetInfo struct {
	Name                string `json:"name"`
	Path                string `json:"path"`
	SHA256              string `json:"sha256"`
	SizeBytes           int64  `json:"size_bytes"`
	RecordedSHA256      string `json:"recorded_sha256"`
	PluginCatalogSHA256 string `json:"openbao_plugin_catalog_sha256,omitempty"`
	IncludedInChecksums bool   `json:"included_in_checksums_txt"`
}

type evidenceFile struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
}

type reproducibilityInfo struct {
	Verified    bool         `json:"verified"`
	Report      evidenceFile `json:"report"`
	PrimaryPath string       `json:"primary_path"`
	RebuildPath string       `json:"rebuild_path"`
}

type attestationInfo struct {
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	SignerWorkflow    string `json:"signer_workflow,omitempty"`
}

type ociPluginImageInfo struct {
	Published bool   `json:"published"`
	Note      string `json:"note"`
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	idx, err := buildIndex(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if err := writeIndex(cfg.indexPath, idx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Wrote %s\n", cfg.indexPath)
}

func parseConfig() (config, error) {
	cfg := config{}

	flag.StringVar(&cfg.indexPath, "index-path", "dist/release/provenance-index.json", "output index path")
	flag.StringVar(&cfg.repo, "repo", "", "repository in owner/repo form")
	flag.StringVar(&cfg.owner, "owner", "", "repository owner")
	flag.StringVar(&cfg.version, "version", "", "release version")
	flag.StringVar(&cfg.pluginVersion, "plugin-version", "", "OpenBao plugin catalog version")
	flag.Int64Var(&cfg.sourceDateEpoch, "source-date-epoch", 0, "deterministic generated_at timestamp")
	flag.StringVar(&cfg.binaryName, "binary-name", "openbao-plugin-secrets-sync", "plugin binary name")
	flag.StringVar(&cfg.releaseSourceRef, "release-source-ref", "", "release source ref")
	flag.StringVar(&cfg.releaseWorkflow, "release-workflow", "", "release workflow identity")
	flag.StringVar(&cfg.checksumsPath, "checksums-path", "dist/release/checksums.txt", "checksums path")
	flag.StringVar(
		&cfg.checksumsBundlePath,
		"checksums-bundle-path",
		"dist/release/checksums.txt.bundle",
		"checksums signature bundle path",
	)
	flag.StringVar(&cfg.sbomGlob, "sbom-glob", "dist/release/sbom-*.spdx.json", "SBOM glob")
	flag.StringVar(
		&cfg.reproducibilityReportPath,
		"reproducibility-report-path",
		"dist/release/reproducibility-report.md",
		"reproducibility report path",
	)
	flag.BoolVar(&cfg.reproducible, "reproducible", true, "whether byte reproducibility was verified")
	flag.BoolVar(
		&cfg.attestationsAvailable,
		"attestations-available",
		true,
		"whether GitHub artifact attestations are available",
	)
	flag.StringVar(
		&cfg.attestationsUnavailableNote,
		"attestations-unavailable-reason",
		"",
		"reason artifact attestations are unavailable",
	)
	flag.Parse()

	return cfg, cfg.validate()
}

func (cfg config) validate() error {
	required := map[string]string{
		"-repo":        cfg.repo,
		"-owner":       cfg.owner,
		"-version":     cfg.version,
		"-binary-name": cfg.binaryName,
	}
	for name, value := range required {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

func buildIndex(cfg config) (provenanceIndex, error) {
	checksumSubjects, err := parseChecksumsFile(cfg.checksumsPath)
	if err != nil {
		return provenanceIndex{}, fmt.Errorf("parse checksums file: %w", err)
	}

	assets, err := buildAssets(filepath.Dir(cfg.checksumsPath), checksumSubjects, cfg.binaryName)
	if err != nil {
		return provenanceIndex{}, err
	}
	sboms, err := buildEvidenceFiles(cfg.sbomGlob)
	if err != nil {
		return provenanceIndex{}, fmt.Errorf("build sbom evidence: %w", err)
	}
	reproReport, err := buildEvidenceFile(cfg.reproducibilityReportPath)
	if err != nil {
		return provenanceIndex{}, fmt.Errorf("build reproducibility report evidence: %w", err)
	}
	checksumsDigest, err := sha256WithPrefix(cfg.checksumsPath)
	if err != nil {
		return provenanceIndex{}, fmt.Errorf("hash checksums path: %w", err)
	}
	bundleDigest, err := sha256WithPrefix(cfg.checksumsBundlePath)
	if err != nil {
		return provenanceIndex{}, fmt.Errorf("hash checksums bundle: %w", err)
	}

	return provenanceIndex{
		SchemaVersion: 1,
		Project: projectInfo{
			Repository: cfg.repo,
			Owner:      cfg.owner,
			Binary:     cfg.binaryName,
		},
		Release: releaseInfo{
			Version:            cfg.version,
			PluginVersion:      effectivePluginVersion(cfg),
			SourceRef:          effectiveSourceRef(cfg),
			SourceDateEpoch:    cfg.sourceDateEpoch,
			GeneratedAtUTC:     generatedAt(cfg.sourceDateEpoch),
			ReleaseWorkflow:    effectiveReleaseWorkflow(cfg),
			ExpectedOIDCIssuer: "https://token.actions.githubusercontent.com",
		},
		Checksums: checksumsInfo{
			Path:                  cfg.checksumsPath,
			Digest:                checksumsDigest,
			SignatureBundlePath:   cfg.checksumsBundlePath,
			SignatureBundleDigest: bundleDigest,
		},
		Assets: assets,
		SBOMs:  sboms,
		Reproducible: reproducibilityInfo{
			Verified:    cfg.reproducible,
			Report:      reproReport,
			PrimaryPath: "dist/release",
			RebuildPath: "dist/rebuild",
		},
		Attestations: attestationInfo{
			Available:         cfg.attestationsAvailable,
			UnavailableReason: attestationUnavailableReason(cfg),
			SignerWorkflow:    attestationSignerWorkflow(cfg),
		},
		OCIPluginImage: ociPluginImageInfo{
			Published: false,
			Note:      "OCI plugin image publishing is not part of this release artifact set.",
		},
	}, nil
}

func effectivePluginVersion(cfg config) string {
	if cfg.pluginVersion != "" {
		return cfg.pluginVersion
	}
	if strings.HasPrefix(cfg.version, "v") {
		return cfg.version
	}
	return "v" + cfg.version
}

func effectiveSourceRef(cfg config) string {
	if cfg.releaseSourceRef != "" {
		return cfg.releaseSourceRef
	}
	return "refs/tags/" + cfg.version
}

func effectiveReleaseWorkflow(cfg config) string {
	if cfg.releaseWorkflow != "" {
		return cfg.releaseWorkflow
	}
	return cfg.repo + "/.github/workflows/release.yml"
}

func generatedAt(sourceDateEpoch int64) string {
	return time.Unix(sourceDateEpoch, 0).UTC().Format(time.RFC3339)
}

func attestationUnavailableReason(cfg config) string {
	if cfg.attestationsAvailable {
		return ""
	}
	if cfg.attestationsUnavailableNote != "" {
		return cfg.attestationsUnavailableNote
	}
	return "GitHub artifact attestations are unavailable for this release context."
}

func attestationSignerWorkflow(cfg config) string {
	if !cfg.attestationsAvailable {
		return ""
	}
	return effectiveReleaseWorkflow(cfg)
}

func parseChecksumsFile(path string) (map[string]string, error) {
	// #nosec G304 -- release tooling reads the caller-provided checksums path.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	subjects := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		subjects[fields[1]] = fields[0]
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(subjects) == 0 {
		return nil, errors.New("checksums file did not contain any subjects")
	}
	return subjects, nil
}

func buildAssets(dir string, checksumSubjects map[string]string, binaryName string) ([]assetInfo, error) {
	names := make([]string, 0, len(checksumSubjects))
	for name := range checksumSubjects {
		names = append(names, name)
	}
	sort.Strings(names)

	assets := make([]assetInfo, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		sha, size, err := fileSHA256(path)
		if err != nil {
			return nil, fmt.Errorf("hash asset %s: %w", path, err)
		}
		recordedSHA := checksumSubjects[name]
		if !strings.EqualFold(recordedSHA, sha) {
			return nil, fmt.Errorf("asset %s checksum mismatch: recorded %s, got %s", name, recordedSHA, sha)
		}
		assets = append(assets, assetInfo{
			Name:                name,
			Path:                path,
			SHA256:              sha,
			SizeBytes:           size,
			RecordedSHA256:      recordedSHA,
			PluginCatalogSHA256: pluginCatalogSHA256(name, binaryName, sha),
			IncludedInChecksums: true,
		})
	}
	return assets, nil
}

func pluginCatalogSHA256(name string, binaryName string, sha string) string {
	if strings.HasPrefix(name, binaryName+"_") {
		return sha
	}
	return ""
}

func buildEvidenceFiles(pattern string) ([]evidenceFile, error) {
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	files := make([]evidenceFile, 0, len(paths))
	for _, path := range paths {
		file, err := buildEvidenceFile(path)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func buildEvidenceFile(path string) (evidenceFile, error) {
	sha, size, err := fileSHA256(path)
	if err != nil {
		return evidenceFile{}, fmt.Errorf("hash evidence file %s: %w", path, err)
	}
	return evidenceFile{
		Name:      filepath.Base(path),
		Path:      path,
		Digest:    "sha256:" + sha,
		SizeBytes: size,
	}, nil
}

func sha256WithPrefix(path string) (string, error) {
	sha, _, err := fileSHA256(path)
	if err != nil {
		return "", err
	}
	return "sha256:" + sha, nil
}

func fileSHA256(path string) (string, int64, error) {
	// #nosec G304 -- release tooling hashes caller-provided release artifact paths.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data)), nil
}

func writeIndex(path string, idx provenanceIndex) error {
	// #nosec G301 -- release evidence is intentionally published as a world-readable artifact.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create index directory: %w", err)
	}

	out, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}
	out = append(out, '\n')

	// #nosec G306 -- release evidence is intentionally published as a world-readable artifact.
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write index: %w", err)
	}
	return nil
}
