// Package instrumentation scans OpenTelemetry Go instrumentation modules and
// produces a versioned inventory of [Library] records. For each module it
// parses the go.mod with [ParseModule] and derives descriptive metadata with
// [DeriveMetadata]. [ScanRepo] ties these steps together over an upstream
// repository checkout.
package instrumentation

import (
	"bytes"
	"cmp"
	"fmt"
	"go/version"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"

	"github.com/open-telemetry/opentelemetry-ecosystem-explorer/golang-instrumentation-watcher/metadata"
)

// Library is the fused per-instrumentation record: [metadata.Metadata] derived
// from go.mod paired with any observed [Telemetry]. It mirrors the
// libraries[].telemetry[] shape used by the other ecosystem watchers.
type Library struct {
	metadata.Metadata `yaml:",inline"`
	Telemetry         []Telemetry `yaml:"telemetry,omitempty"` // spans and metrics emitted by the library
}

// ScanResult holds the output of a repository scan: the fused [Library] records
// that make up the versioned inventory.
type ScanResult struct {
	Libraries []Library // instrumentation libraries discovered in the scan
}

// ScanRepo walks the go-contrib repository rooted at repoPath and returns the
// fused [Library] records discovered within it. It scans only the subtrees that
// instrument a developer's code: instrumentation wrappers (gin, grpc, http…)
// and bridges (zap, logrus…). The other go-contrib components (exporters,
// propagators, samplers, detectors, processors) configure the SDK pipeline
// rather than instrument a target library, so they have no target_module and
// are out of scope here.
//
// A scan error is returned rather than swallowed: a missing subtree or an
// unreadable go.mod would otherwise produce a smaller inventory that commits as
// a spurious "libraries removed" diff. The returned libraries are sorted by name
// for a byte-stable inventory.
func ScanRepo(repoPath string) (*ScanResult, error) {
	var libraries []Library
	for _, sub := range []string{"instrumentation", "bridges"} {
		packages, err := Walk(filepath.Join(repoPath, sub))
		if err != nil {
			return nil, err
		}
		for _, pkg := range packages {
			lib, err := analyzeLibrary(pkg.GoModPath)
			if err != nil {
				return nil, err
			}
			if lib == nil {
				continue
			}
			libraries = append(libraries, *lib)
		}
	}

	slices.SortFunc(libraries, func(a, b Library) int { return cmp.Compare(a.Name, b.Name) })

	return &ScanResult{Libraries: libraries}, nil
}

// analyzeLibrary builds the [Library] for a single instrumentation module.
// Metadata is derived from the module's own go.mod directive via
// [DeriveMetadata]. It returns nil (and nil error) for modules that are not
// go-contrib requires.
func analyzeLibrary(goModPath string) (*Library, error) {
	mod, err := ParseModule(goModPath)
	if err != nil {
		return nil, err
	}
	if mod.Path == "" || !IsOTelContribRequire(mod.Path) {
		return nil, nil
	}
	meta := DeriveMetadata(mod)
	return &Library{Metadata: *meta}, nil
}

// ScanMetadataRepo walks the repository rooted at repoPath looking for
// metadata.yaml files. It parses each file into a [Library] record, fusing
// it into a [ScanResult].
func ScanMetadataRepo(repoPath string) (*ScanResult, error) {
	metaFiles, err := WalkMetadata(repoPath)
	if err != nil {
		return nil, err
	}

	// Pre-scan all go.mod files to infer GoMinVersion if blank.
	modVersions := make(map[string]string)
	if packages, err := Walk(repoPath); err == nil {
		for _, pkg := range packages {
			mod, err := ParseModule(pkg.GoModPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to parse go.mod %s: %v\n", pkg.GoModPath, err)
				continue
			}
			if mod.Path == "" {
				fmt.Fprintf(os.Stderr, "missing module path in go.mod: %s\n", pkg.GoModPath)
				continue
			}

			modVersions[mod.Path] = mod.GoVersion
		}
	} else {
		fmt.Fprintf(os.Stderr, "failed to walk repository for go.mod files: %v\n", err)
	}

	var libraries []Library
	seenNames := make(map[string]bool)
	var catalog map[string]metadata.Configuration
	catalogLoaded := false

	for _, mf := range metaFiles {
		lib, err := analyzeMetadataLibrary(mf.MetadataPath)
		if err != nil {
			return nil, err
		}
		if lib == nil {
			continue
		}

		if lib.Name == "" {
			return nil, fmt.Errorf("empty name field in %s", mf.MetadataPath)
		}
		if seenNames[lib.Name] {
			return nil, fmt.Errorf("duplicate library name %q in %s", lib.Name, mf.MetadataPath)
		}
		seenNames[lib.Name] = true

		if lib.SourcePath == "" {
			lib.SourcePath = mf.Path
		}

		// Infer GoMinVersion from go.mod if not specified in metadata.
		lib.inferGoMinVersion(modVersions)

		// Strictly validate explicitly authored versions
		if err := lib.validateVersions(mf.MetadataPath); err != nil {
			return nil, err
		}
		if err := resolveConfigurationRefs(lib, repoPath, mf.MetadataPath, &catalog, &catalogLoaded); err != nil {
			return nil, err
		}

		libraries = append(libraries, *lib)
	}

	slices.SortFunc(libraries, func(a, b Library) int { return cmp.Compare(a.Name, b.Name) })

	return &ScanResult{Libraries: libraries}, nil
}

func (l *Library) validateVersions(metadataPath string) error {
	if l.GoMinVersion != "" {
		if !version.IsValid("go" + l.GoMinVersion) {
			return fmt.Errorf("invalid go_min_version %q in %s", l.GoMinVersion, metadataPath)
		}
	}

	if l.OtelcMinVersion != "" {
		if !semver.IsValid(l.OtelcMinVersion) {
			return fmt.Errorf("invalid otelc_min_version %q in %s: must be a valid semver", l.OtelcMinVersion, metadataPath)
		}
	}

	for _, m := range l.Modules {
		if m.Version != "" && !semver.IsValid(m.Version) {
			return fmt.Errorf("invalid module version %q in %s", m.Version, metadataPath)
		}
	}
	return nil
}

func (l *Library) inferGoMinVersion(modVersions map[string]string) {
	if l.GoMinVersion != "" {
		return
	}
	var highest string
	for _, m := range l.Modules {
		if gv, ok := modVersions[m.Path]; ok && gv != "" {
			if highest == "" || version.Compare("go"+gv, "go"+highest) > 0 {
				highest = gv
			}
		}
	}
	l.GoMinVersion = highest
}

func analyzeMetadataLibrary(metadataPath string) (*Library, error) {
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", metadataPath, err)
	}

	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %w", metadataPath, err)
	}
	if err := validateConfigurationSourceNodes(&document, metadataPath); err != nil {
		return nil, err
	}

	var lib Library
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&lib); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %w", metadataPath, err)
	}
	if err := validateConfigurationValues(lib.Configurations, metadataPath); err != nil {
		return nil, err
	}

	return &lib, nil
}

// validateConfigurationSourceNodes validates the source YAML before refs are
// expanded. Checking the nodes preserves the distinction between an omitted
// field and a present-but-empty field, which the inventory structs can't make.
// Every entry requires a name (inline definition) or a ref, never both.
func validateConfigurationSourceNodes(document *yaml.Node, metadataPath string) error {
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("invalid metadata document in %s: expected a mapping", metadataPath)
	}
	var configurationsNode *yaml.Node
	root := document.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != "configurations" {
			continue
		}
		if configurationsNode != nil {
			return fmt.Errorf("duplicate configurations key in %s", metadataPath)
		}
		configurationsNode = root.Content[i+1]
	}
	if configurationsNode == nil {
		return nil
	}
	if configurationsNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("configurations in %s must be a sequence", metadataPath)
	}
	for i, node := range configurationsNode.Content {
		entrySource := fmt.Sprintf("invalid configurations[%d] in %s", i, metadataPath)
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: expected a mapping", entrySource)
		}
		fields := make(map[string]*yaml.Node, len(node.Content)/2)
		for j := 0; j < len(node.Content); j += 2 {
			keyNode, valueNode := node.Content[j], node.Content[j+1]
			if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" {
				return fmt.Errorf("%s: configuration keys must be strings", entrySource)
			}
			if _, exists := fields[keyNode.Value]; exists {
				return fmt.Errorf("%s: duplicate field %q", entrySource, keyNode.Value)
			}
			fields[keyNode.Value] = valueNode
		}

		_, hasName := fields["name"]
		_, hasRef := fields["ref"]
		if hasName == hasRef {
			return fmt.Errorf("%s: exactly one of name or ref must be set", entrySource)
		}
		if hasName {
			if err := validateSelectorString(fields["name"], "name", entrySource); err != nil {
				return err
			}
		}
		if hasRef {
			if err := validateSelectorString(fields["ref"], "ref", entrySource); err != nil {
				return err
			}
			for _, field := range []string{"name", "description", "type", "default", "examples", "schema", "default_behavior"} {
				if _, exists := fields[field]; exists {
					return fmt.Errorf("%s: a ref entry must not include inline configuration field %q", entrySource, field)
				}
			}
		}
		for _, field := range []string{"name", "description", "type", "default_behavior"} {
			if value, exists := fields[field]; exists && (value.Kind != yaml.ScalarNode || value.Tag != "!!str") {
				return fmt.Errorf("%s: %s must be a string", entrySource, field)
			}
		}
		if defaultNode, exists := fields["default"]; exists &&
			!isDefaultScalar(defaultNode) {
			return fmt.Errorf("%s: default must be a string, boolean, integer, or float scalar", entrySource)
		}
		if hasName {
			typeNode, exists := fields["type"]
			if !exists || typeNode.Kind != yaml.ScalarNode || typeNode.Tag != "!!str" {
				return fmt.Errorf("%s: type must be present and a string", entrySource)
			}
			if !isSupportedConfigurationType(typeNode.Value) {
				return fmt.Errorf("%s: unsupported type %q", entrySource, typeNode.Value)
			}
		}
		if schema, exists := fields["schema"]; exists && schema.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: schema must be a mapping", entrySource)
		} else if exists {
			if err := validateSchemaNodes(schema, entrySource); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateConfigurationValues(configurations []metadata.Configuration, metadataPath string) error {
	for i, configuration := range configurations {
		entrySource := fmt.Sprintf("invalid configurations[%d] in %s", i, metadataPath)
		if configuration.Ref != nil && configuration.Name == "" {
			continue
		}
		if !isSupportedConfigurationType(configuration.Type) {
			return fmt.Errorf("%s: unsupported or missing type %q", entrySource, configuration.Type)
		}
		if configuration.Default != nil {
			switch configuration.Type {
			case "boolean":
				if _, err := strconv.ParseBool(*configuration.Default); err != nil {
					return fmt.Errorf("%s: default %q is not a valid boolean", entrySource, *configuration.Default)
				}
			case "int":
				if _, err := strconv.ParseInt(*configuration.Default, 10, 64); err != nil {
					return fmt.Errorf("%s: default %q is not a valid integer", entrySource, *configuration.Default)
				}
			}
		}
		if configuration.Schema != nil && configuration.Type != "structured_list" {
			return fmt.Errorf("%s: schema requires type structured_list", entrySource)
		}
		if configuration.Type == "structured_list" && configuration.Schema == nil {
			return fmt.Errorf("%s: structured_list requires schema", entrySource)
		}
		if configuration.Schema != nil && configuration.Schema.Type != "object" {
			return fmt.Errorf("%s: schema type must be object", entrySource)
		}
		if configuration.Schema != nil {
			if configuration.Schema.Properties == nil {
				return fmt.Errorf("%s: schema properties must be present", entrySource)
			}
			seen := make(map[string]bool)
			for _, key := range configuration.Schema.Required {
				if seen[key] {
					return fmt.Errorf("%s: schema required has duplicate key %q", entrySource, key)
				}
				seen[key] = true
				if _, exists := configuration.Schema.Properties[key]; !exists {
					return fmt.Errorf("%s: schema required key %q is not defined", entrySource, key)
				}
			}
		}
		if configuration.Default != nil && (configuration.Type == "map" || configuration.Type == "list" || configuration.Type == "structured_list") {
			if err := validateCollectionDefault(configuration, entrySource); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCollectionDefault(configuration metadata.Configuration, source string) error {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(*configuration.Default), &document); err != nil {
		return fmt.Errorf("%s: parse collection default: %w", source, err)
	}
	if len(document.Content) != 1 || !schemaValueMatches(document.Content[0], configuration.Type) {
		return fmt.Errorf("%s: default must encode a YAML value of type %q", source, configuration.Type)
	}
	// Decode as well to catch duplicate keys inside the encoded default.
	var decoded any
	if err := document.Decode(&decoded); err != nil {
		return fmt.Errorf("%s: invalid collection default: %w", source, err)
	}
	if configuration.Type != "structured_list" {
		return nil
	}
	for i, item := range document.Content[0].Content {
		fields := make(map[string]*yaml.Node, len(item.Content)/2)
		for j := 0; j < len(item.Content); j += 2 {
			fields[item.Content[j].Value] = item.Content[j+1]
		}
		for _, required := range configuration.Schema.Required {
			if _, exists := fields[required]; !exists {
				return fmt.Errorf("%s: default[%d] required schema property %q is missing", source, i, required)
			}
		}
		for j := 0; j < len(item.Content); j += 2 {
			key, value := item.Content[j].Value, item.Content[j+1]
			property, exists := configuration.Schema.Properties[key]
			if !exists || !schemaValueMatches(value, property.Type) {
				return fmt.Errorf("%s: default[%d] schema property %q is unknown or has the wrong type", source, i, key)
			}
		}
	}
	return nil
}

// validateSchemaNodes preserves presence and native types for schema property
// defaults and examples; decoding a missing and an explicit null both yield nil.
func validateSchemaNodes(schema *yaml.Node, source string) error {
	for i := 0; i < len(schema.Content); i += 2 {
		if schema.Content[i].Value != "properties" {
			continue
		}
		properties := schema.Content[i+1]
		if properties.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: schema properties must be a mapping", source)
		}
		for j := 0; j < len(properties.Content); j += 2 {
			key, property := properties.Content[j], properties.Content[j+1]
			if key.Tag != "!!str" || key.Value == "" || property.Kind != yaml.MappingNode {
				return fmt.Errorf("%s: schema properties require non-empty string keys and mapping values", source)
			}
			var typeNode *yaml.Node
			for k := 0; k < len(property.Content); k += 2 {
				if property.Content[k].Value == "type" {
					typeNode = property.Content[k+1]
				}
			}
			if typeNode == nil || typeNode.Tag != "!!str" || !isSupportedConfigurationType(typeNode.Value) {
				return fmt.Errorf("%s: schema property %q requires a supported type", source, key.Value)
			}
			for k := 0; k < len(property.Content); k += 2 {
				field, value := property.Content[k].Value, property.Content[k+1]
				if (field == "default" || field == "example") && !schemaValueMatches(value, typeNode.Value) {
					return fmt.Errorf("%s: schema property %q %s does not match type %q", source, key.Value, field, typeNode.Value)
				}
			}
		}
	}
	return nil
}

func schemaValueMatches(node *yaml.Node, typeName string) bool {
	switch typeName {
	case "boolean":
		return node.Kind == yaml.ScalarNode && node.Tag == "!!bool"
	case "int":
		return node.Kind == yaml.ScalarNode && node.Tag == "!!int"
	case "string":
		return node.Kind == yaml.ScalarNode && node.Tag == "!!str"
	case "map":
		if node.Kind != yaml.MappingNode {
			return false
		}
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Tag != "!!str" {
				return false
			}
		}
		return true
	case "list":
		return node.Kind == yaml.SequenceNode
	case "structured_list":
		if node.Kind != yaml.SequenceNode {
			return false
		}
		for _, item := range node.Content {
			if !schemaValueMatches(item, "map") {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func validateSelectorString(node *yaml.Node, field, source string) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return fmt.Errorf("%s: %s must be a string", source, field)
	}
	if strings.TrimSpace(node.Value) == "" {
		return fmt.Errorf("%s: %s must not be empty", source, field)
	}
	if strings.TrimSpace(node.Value) != node.Value {
		return fmt.Errorf("%s: %s must not have leading or trailing whitespace", source, field)
	}
	return nil
}

func isDefaultScalar(node *yaml.Node) bool {
	if node.Kind != yaml.ScalarNode {
		return false
	}
	switch node.Tag {
	case "!!str", "!!bool", "!!int", "!!float":
		return true
	default:
		return false
	}
}

func resolveConfigurationRefs(
	library *Library,
	repoPath string,
	metadataPath string,
	catalog *map[string]metadata.Configuration,
	catalogLoaded *bool,
) error {
	for i, sourceConfiguration := range library.Configurations {
		if sourceConfiguration.Ref == nil {
			continue
		}
		if !*catalogLoaded {
			definitions, err := readConfigurationCatalog(repoPath)
			if err != nil {
				return fmt.Errorf("resolve configurations in %s: %w", metadataPath, err)
			}
			*catalog = definitions
			*catalogLoaded = true
		}
		definition, exists := (*catalog)[*sourceConfiguration.Ref]
		if !exists {
			return fmt.Errorf("unknown configuration ref %q in %s: no matching definition in %s", *sourceConfiguration.Ref, metadataPath, filepath.Join(repoPath, filepath.FromSlash(configurationCatalogPath)))
		}
		resolved := cloneConfiguration(definition)
		ref := *sourceConfiguration.Ref
		resolved.Ref = &ref
		library.Configurations[i] = resolved
	}
	return nil
}

func isSupportedConfigurationType(value string) bool {
	switch value {
	case "boolean", "string", "int", "map", "list", "structured_list":
		return true
	default:
		return false
	}
}
