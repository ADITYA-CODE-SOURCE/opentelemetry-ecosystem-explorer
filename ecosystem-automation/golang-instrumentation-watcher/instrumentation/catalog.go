package instrumentation

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/open-telemetry/opentelemetry-ecosystem-explorer/golang-instrumentation-watcher/metadata"
)

// DefaultConfigurationCatalogPath is the location of the central catalog in
// the otelc repository. The watcher loads it separately from scanned repos.
const DefaultConfigurationCatalogPath = "tool/internal/configuration/catalog.yaml"

// ConfigurationCatalog contains the central ref definitions supplied to a
// metadata scan. SourcePath is retained for diagnostics.
type ConfigurationCatalog struct {
	SourcePath  string
	Definitions map[string]metadata.Configuration
}

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

// LoadConfigurationCatalog loads a catalog from its centrally configured path.
func LoadConfigurationCatalog(path string) (*ConfigurationCatalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read configuration catalog %s: %w", path, err)
	}
	definitions, err := parseConfigurationCatalog(data, path)
	if err != nil {
		return nil, err
	}
	return &ConfigurationCatalog{SourcePath: path, Definitions: definitions}, nil
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

	for _, ref := range slices.Sorted(maps.Keys(catalog.Definitions)) {
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
