package instrumentation

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/open-telemetry/opentelemetry-ecosystem-explorer/golang-instrumentation-watcher/metadata"
)

// configurationCatalogPath is relative to the root of the checked-out otelc
// repository. The Go watcher scans that same checkout, so the catalog and
// metadata.yaml are always read from one revision without network access.
const configurationCatalogPath = "tool/internal/configuration/catalog.yaml"

type configurationCatalog struct {
	CatalogVersion int                               `yaml:"catalog_version"`
	Source         configurationCatalogSource        `yaml:"source"`
	Definitions    map[string]metadata.Configuration `yaml:"definitions"`
}

type configurationCatalogSource struct {
	Repository string `yaml:"repository"`
	Release    string `yaml:"release"`
	Commit     string `yaml:"commit"`
	File       string `yaml:"file"`
}

func readConfigurationCatalog(repoPath string) (map[string]metadata.Configuration, error) {
	path := filepath.Join(repoPath, filepath.FromSlash(configurationCatalogPath))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read configuration catalog %s: %w", path, err)
	}
	return parseConfigurationCatalog(data, path)
}

func parseConfigurationCatalog(data []byte, path string) (map[string]metadata.Configuration, error) {
	var catalog configurationCatalog
	if err := decodeSingleYAMLDocument(data, &catalog); err != nil {
		return nil, fmt.Errorf("parse configuration catalog %s: %w", path, err)
	}
	if catalog.CatalogVersion != 1 {
		return nil, fmt.Errorf("unsupported configuration catalog version %d in %s", catalog.CatalogVersion, path)
	}
	if catalog.Source.Repository != "open-telemetry/opentelemetry-configuration" ||
		catalog.Source.Release == "" || catalog.Source.Commit == "" ||
		catalog.Source.File != "schema/instrumentation.yaml" {
		return nil, fmt.Errorf("configuration catalog %s must identify a pinned OpenTelemetry Configuration instrumentation schema", path)
	}
	if len(catalog.Definitions) == 0 {
		return nil, fmt.Errorf("configuration catalog %s has no definitions", path)
	}

	refs := make([]string, 0, len(catalog.Definitions))
	for ref := range catalog.Definitions {
		refs = append(refs, ref)
	}
	// Stable validation order makes the first reported invalid ref deterministic.
	slices.Sort(refs)
	for _, ref := range refs {
		definition := catalog.Definitions[ref]
		source := fmt.Sprintf("configuration catalog %s ref %q", path, ref)
		if definition.Ref != nil {
			return nil, fmt.Errorf("%s must define a path directly, not another ref", source)
		}
		if ref == "" || definition.Name == "" {
			return nil, fmt.Errorf("%s must have a non-empty name", source)
		}
		if definition.Name != ref {
			return nil, fmt.Errorf("%s key must equal name %q", source, definition.Name)
		}
		if !strings.HasPrefix(ref, "general.") && !strings.HasPrefix(ref, "go.") {
			return nil, fmt.Errorf("%s must use the general. or go. namespace", source)
		}
		if err := validateConfigurationValues([]metadata.Configuration{definition}, source); err != nil {
			return nil, err
		}
	}
	return catalog.Definitions, nil
}
