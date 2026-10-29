// Package instrumentation scans OpenTelemetry Go instrumentation modules and
// produces a versioned inventory of [Library] records. For each module it
// parses the go.mod with [ParseModule] and derives descriptive metadata with
// [DeriveMetadata]. [ScanRepo] ties these steps together over an upstream
// repository checkout.
package instrumentation

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"go/version"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
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
func ScanMetadataRepo(repoPath string, catalog *ConfigurationCatalog) (*ScanResult, error) {
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
		if err := resolveConfigurationRefs(lib, catalog, mf.MetadataPath); err != nil {
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

	var lib Library
	if err := decodeSingleYAMLDocument(data, &lib); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %w", metadataPath, err)
	}
	if err := validateConfigurationValues(lib.Configurations, metadataPath); err != nil {
		return nil, err
	}

	return &lib, nil
}

func validateConfigurationValues(configurations []metadata.Configuration, metadataPath string) error {
	for i, configuration := range configurations {
		entrySource := fmt.Sprintf("invalid configurations[%d] in %s", i, metadataPath)
		hasName := configuration.Name != ""
		hasRef := configuration.Ref != nil
		if hasName == hasRef {
			return fmt.Errorf("%s: exactly one of name or ref must be set", entrySource)
		}
		if hasName && strings.TrimSpace(configuration.Name) != configuration.Name {
			return fmt.Errorf("%s: name must not have leading or trailing whitespace", entrySource)
		}
		if hasName && !isConfigurationPath(configuration.Name) {
			return fmt.Errorf("%s: name must be a canonical general. or go. configuration path", entrySource)
		}
		if hasRef {
			ref := *configuration.Ref
			if strings.TrimSpace(ref) == "" {
				return fmt.Errorf("%s: ref must not be empty", entrySource)
			}
			if strings.TrimSpace(ref) != ref {
				return fmt.Errorf("%s: ref must not have leading or trailing whitespace", entrySource)
			}
			if !isConfigurationPath(ref) {
				return fmt.Errorf("%s: ref must be a canonical general. or go. configuration path", entrySource)
			}
			for _, inlineField := range []struct {
				name string
				set  bool
			}{
				{name: "name", set: configuration.Name != ""},
				{name: "description", set: configuration.Description != ""},
				{name: "type", set: configuration.Type != ""},
				{name: "default", set: configuration.Default != nil},
				{name: "examples", set: len(configuration.Examples) != 0},
				{name: "schema", set: configuration.Schema != nil},
				{name: "default_behavior", set: configuration.DefaultBehavior != ""},
				{name: "deprecated", set: configuration.Deprecated},
				{name: "replaced_by", set: configuration.ReplacedBy != nil},
			} {
				if inlineField.set {
					return fmt.Errorf("%s: a ref entry must not include inline configuration field %q", entrySource, inlineField.name)
				}
			}
			continue
		}
		if !isSupportedConfigurationType(configuration.Type) {
			return fmt.Errorf("%s: unsupported or missing type %q", entrySource, configuration.Type)
		}
		if configuration.ReplacedBy != nil {
			if !configuration.Deprecated {
				return fmt.Errorf("%s: replaced_by requires deprecated to be true", entrySource)
			}
			replacement := *configuration.ReplacedBy
			if strings.TrimSpace(replacement) == "" {
				return fmt.Errorf("%s: replaced_by must not be empty", entrySource)
			}
			if strings.TrimSpace(replacement) != replacement {
				return fmt.Errorf("%s: replaced_by must not have leading or trailing whitespace", entrySource)
			}
			if !isConfigurationPath(replacement) {
				return fmt.Errorf("%s: replaced_by must be a canonical general. or go. configuration path", entrySource)
			}
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
			for _, name := range slices.Sorted(maps.Keys(configuration.Schema.Properties)) {
				property := configuration.Schema.Properties[name]
				propertySource := fmt.Sprintf("%s: schema property %q", entrySource, name)
				if name == "" {
					return fmt.Errorf("%s: property name must not be empty", propertySource)
				}
				if !isSupportedConfigurationType(property.Type) {
					return fmt.Errorf("%s has unsupported type %q", propertySource, property.Type)
				}
				if property.Default != nil && !configurationValueMatchesType(property.Default, property.Type) {
					return fmt.Errorf("%s default does not match type %q", propertySource, property.Type)
				}
				if property.Example != nil && !configurationValueMatchesType(property.Example, property.Type) {
					return fmt.Errorf("%s example does not match type %q", propertySource, property.Type)
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
	var value any
	if err := decodeSingleYAMLDocument([]byte(*configuration.Default), &value); err != nil {
		return fmt.Errorf("%s: parse collection default: %w", source, err)
	}
	if !configurationValueMatchesType(value, configuration.Type) {
		return fmt.Errorf("%s: default must encode a YAML value of type %q", source, configuration.Type)
	}
	if configuration.Type != "structured_list" {
		return nil
	}
	items := value.([]any)
	for i, item := range items {
		fields, _ := stringMap(item)
		for _, required := range configuration.Schema.Required {
			if _, exists := fields[required]; !exists {
				return fmt.Errorf("%s: default[%d] required schema property %q is missing", source, i, required)
			}
		}
		for _, key := range slices.Sorted(maps.Keys(fields)) {
			propertyValue := fields[key]
			property, exists := configuration.Schema.Properties[key]
			if !exists || !configurationValueMatchesType(propertyValue, property.Type) {
				return fmt.Errorf("%s: default[%d] schema property %q is unknown or has the wrong type", source, i, key)
			}
		}
	}
	return nil
}

func configurationValueMatchesType(value any, typeName string) bool {
	if value == nil {
		return false
	}
	switch typeName {
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "int":
		kind := reflect.TypeOf(value).Kind()
		return kind >= reflect.Int && kind <= reflect.Int64 || kind >= reflect.Uint && kind <= reflect.Uint64
	case "string":
		_, ok := value.(string)
		return ok
	case "map":
		_, ok := stringMap(value)
		return ok
	case "list":
		kind := reflect.TypeOf(value).Kind()
		return kind == reflect.Slice || kind == reflect.Array
	case "structured_list":
		items, ok := value.([]any)
		if !ok {
			return false
		}
		for _, item := range items {
			if _, ok := stringMap(item); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func stringMap(value any) (map[string]any, bool) {
	if values, ok := value.(map[string]any); ok {
		return values, true
	}
	mapValue := reflect.ValueOf(value)
	if !mapValue.IsValid() || mapValue.Kind() != reflect.Map || mapValue.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	result := make(map[string]any, mapValue.Len())
	iterator := mapValue.MapRange()
	for iterator.Next() {
		result[iterator.Key().String()] = iterator.Value().Interface()
	}
	return result, true
}

func decodeSingleYAMLDocument(data []byte, value any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(value); err != nil {
		return err
	}
	return requireSingleYAMLDocument(decoder)
}

func requireSingleYAMLDocument(decoder *yaml.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("must contain exactly one YAML document")
}

func resolveConfigurationRefs(library *Library, catalog *ConfigurationCatalog, metadataPath string) error {
	for i, sourceConfiguration := range library.Configurations {
		if sourceConfiguration.Ref == nil {
			continue
		}
		ref := *sourceConfiguration.Ref
		if !isConfigurationPath(ref) {
			return fmt.Errorf("invalid configurations[%d] in %s: ref must be a canonical general. or go. configuration path", i, metadataPath)
		}
		if catalog == nil {
			return fmt.Errorf("configuration ref %q in %s requires a centrally loaded catalog", ref, metadataPath)
		}
		definition, exists := catalog.Definitions[ref]
		if !exists {
			return fmt.Errorf("unknown configuration ref %q in %s: no matching definition in %s", ref, metadataPath, catalog.SourcePath)
		}
		resolved := definition
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

func isConfigurationPath(value string) bool {
	if strings.HasPrefix(value, "general.") {
		return len(value) > len("general.") && !strings.HasSuffix(value, "/development")
	}
	return strings.HasPrefix(value, "go.") && len(value) > len("go.")
}
