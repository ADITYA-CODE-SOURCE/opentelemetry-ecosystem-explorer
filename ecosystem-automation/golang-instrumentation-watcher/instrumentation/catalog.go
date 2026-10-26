package instrumentation

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

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

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var catalog configurationCatalog
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("parse configuration catalog %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("configuration catalog %s must contain one YAML document", path)
		}
		return nil, fmt.Errorf("parse trailing configuration catalog document %s: %w", path, err)
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
	slices.Sort(refs)
	definitions := make(map[string]metadata.Configuration, len(refs))
	for _, ref := range refs {
		definition := catalog.Definitions[ref]
		source := fmt.Sprintf("configuration catalog %s ref %q", path, ref)
		if ref == "" || definition.DeclarativeName == nil || *definition.DeclarativeName == "" {
			return nil, fmt.Errorf("%s must have a non-empty declarative_name", source)
		}
		if *definition.DeclarativeName != ref {
			return nil, fmt.Errorf("%s key must equal declarative_name %q", source, *definition.DeclarativeName)
		}
		if !strings.HasPrefix(ref, "general.") && !strings.HasPrefix(ref, "go.") {
			return nil, fmt.Errorf("%s must use the general. or go. namespace", source)
		}
		if definition.Ref != nil {
			return nil, fmt.Errorf("%s must define a path directly, not another ref", source)
		}
		if definition.DeclarativeType != nil && !isSupportedDeclarativeType(*definition.DeclarativeType) {
			return nil, fmt.Errorf("%s has unsupported declarative_type %q", source, *definition.DeclarativeType)
		}
		if definition.DeclarativeSchema != nil &&
			(definition.DeclarativeType == nil || *definition.DeclarativeType != "structured_list") {
			return nil, fmt.Errorf("%s declarative_schema requires declarative_type structured_list", source)
		}
		if definition.DeclarativeType != nil && *definition.DeclarativeType == "structured_list" && definition.DeclarativeSchema == nil {
			return nil, fmt.Errorf("%s structured_list requires declarative_schema", source)
		}
		definitions[ref] = cloneConfiguration(definition)
	}
	return definitions, nil
}

func cloneConfiguration(configuration metadata.Configuration) metadata.Configuration {
	clone := configuration
	clone.Examples = slices.Clone(configuration.Examples)
	if configuration.DeclarativeName != nil {
		value := *configuration.DeclarativeName
		clone.DeclarativeName = &value
	}
	if configuration.Ref != nil {
		value := *configuration.Ref
		clone.Ref = &value
	}
	if configuration.Default != nil {
		value := *configuration.Default
		clone.Default = &value
	}
	if configuration.DeclarativeType != nil {
		value := *configuration.DeclarativeType
		clone.DeclarativeType = &value
	}
	if configuration.DeclarativeSchema != nil {
		schema := *configuration.DeclarativeSchema
		schema.Required = slices.Clone(configuration.DeclarativeSchema.Required)
		schema.Properties = make(map[string]metadata.DeclarativeProperty, len(configuration.DeclarativeSchema.Properties))
		for key, property := range configuration.DeclarativeSchema.Properties {
			schema.Properties[key] = property
		}
		clone.DeclarativeSchema = &schema
	}
	return clone
}
