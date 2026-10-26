package instrumentation

import (
	"bytes"
	"errors"
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
	return parseConfigurationCatalog(data, path)
}

func parseConfigurationCatalog(data []byte, path string) (map[string]metadata.Configuration, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse configuration catalog %s: %w", path, err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("configuration catalog %s must be a mapping", path)
	}
	for i := 0; i < len(document.Content[0].Content); i += 2 {
		if document.Content[0].Content[i].Value != "definitions" {
			continue
		}
		definitions := document.Content[0].Content[i+1]
		if definitions.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("configuration catalog %s definitions must be a mapping", path)
		}
		for j := 0; j < len(definitions.Content); j += 2 {
			// Catalog definitions obey the same source-entry rules as inline
			// metadata. Validate nodes before decoding to preserve field presence.
			entry := definitions.Content[j+1]
			entryDocument := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{
				Kind: yaml.MappingNode, Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "configurations"},
					{Kind: yaml.SequenceNode, Content: []*yaml.Node{entry}},
				},
			}}}
			if err := validateConfigurationSourceNodes(entryDocument, path+" ref "+definitions.Content[j].Value); err != nil {
				return nil, err
			}
		}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var catalog configurationCatalog
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("parse configuration catalog %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
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
		if ref == "" || definition.Name == "" {
			return nil, fmt.Errorf("%s must have a non-empty name", source)
		}
		if definition.Name != ref {
			return nil, fmt.Errorf("%s key must equal name %q", source, definition.Name)
		}
		if !strings.HasPrefix(ref, "general.") && !strings.HasPrefix(ref, "go.") {
			return nil, fmt.Errorf("%s must use the general. or go. namespace", source)
		}
		if definition.Ref != nil {
			return nil, fmt.Errorf("%s must define a path directly, not another ref", source)
		}
		if err := validateConfigurationValues([]metadata.Configuration{definition}, source); err != nil {
			return nil, err
		}
		definitions[ref] = cloneConfiguration(definition)
	}
	return definitions, nil
}

func cloneConfiguration(configuration metadata.Configuration) metadata.Configuration {
	clone := configuration
	clone.Examples = slices.Clone(configuration.Examples)
	if configuration.Ref != nil {
		value := *configuration.Ref
		clone.Ref = &value
	}
	if configuration.Default != nil {
		value := *configuration.Default
		clone.Default = &value
	}
	if configuration.Schema != nil {
		schema := *configuration.Schema
		schema.Required = slices.Clone(configuration.Schema.Required)
		schema.Properties = make(map[string]metadata.ConfigurationProperty, len(configuration.Schema.Properties))
		for key, property := range configuration.Schema.Properties {
			property.Default = cloneYAMLValue(property.Default)
			property.Example = cloneYAMLValue(property.Example)
			schema.Properties[key] = property
		}
		clone.Schema = &schema
	}
	return clone
}

func cloneYAMLValue(value any) any {
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = cloneYAMLValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = cloneYAMLValue(item)
		}
		return result
	case map[any]any:
		result := make(map[any]any, len(value))
		for key, item := range value {
			result[key] = cloneYAMLValue(item)
		}
		return result
	default:
		return value
	}
}
