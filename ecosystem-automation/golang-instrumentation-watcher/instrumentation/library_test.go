package instrumentation

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/open-telemetry/opentelemetry-ecosystem-explorer/golang-instrumentation-watcher/metadata"
)

const (
	dirPerm  = 0o744
	filePerm = 0o644
)

// modFile returns minimal go.mod source declaring modulePath.
func modFile(modulePath string) string {
	return "module " + modulePath + "\n\ngo 1.24.0\n"
}

// writeModule writes a fixture module (go.mod + doc.go) in a fresh temp dir and
// returns the path to its go.mod.
func writeModule(t *testing.T, modulePath string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modFile(modulePath)), filePerm); err != nil {
		t.Fatal(err)
	}
	pkgName := filepath.Base(modulePath)
	doc := "// Package " + pkgName + " is a test fixture.\npackage " + pkgName + "\n"
	if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte(doc), filePerm); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "go.mod")
}

// writeGoMod writes content as a go.mod under root/relDir, creating the dir.
func writeGoMod(t *testing.T, root, relDir, content string) {
	t.Helper()
	dir := filepath.Join(root, relDir)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), filePerm); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyzeLibrary(t *testing.T) {
	tests := []struct {
		name       string
		modulePath string
		wantNil    bool
		wantName   string
		wantType   metadata.InstrType
	}{
		{
			name:       "contrib instrumentation module",
			modulePath: "go.opentelemetry.io/contrib/instrumentation/example.com/widget/otelwidget",
			wantName:   "instrumentation-example.com-widget-otelwidget",
			wantType:   metadata.InstrTypeWrapper,
		},
		{
			name:       "non-contrib module",
			modulePath: "example.com/not/contrib/thing",
			wantNil:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lib, err := analyzeLibrary(writeModule(t, tt.modulePath))
			if err != nil {
				t.Fatalf("analyzeLibrary() error = %v", err)
			}
			if tt.wantNil {
				if lib != nil {
					t.Errorf("analyzeLibrary() = %+v, want nil", lib)
				}
				return
			}
			if lib == nil {
				t.Fatal("analyzeLibrary() returned nil, want non-nil")
			}
			if lib.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", lib.Name, tt.wantName)
			}
			if lib.InstrumentationType != tt.wantType {
				t.Errorf("InstrumentationType = %v, want %v", lib.InstrumentationType, tt.wantType)
			}
			if len(lib.Telemetry) != 0 {
				t.Errorf("Telemetry = %d entries, want 0 (no analyzer in extract phase)", len(lib.Telemetry))
			}
		})
	}
}

// writeMetadata writes content as a metadata.yaml under root/relDir, creating the dir.
func writeMetadata(t *testing.T, root, relDir, content string) {
	t.Helper()
	dir := filepath.Join(root, relDir)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.yaml"), []byte(content), filePerm); err != nil {
		t.Fatal(err)
	}
}

func writeConfigurationCatalog(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(DefaultConfigurationCatalogPath))
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		t.Fatal(err)
	}
	const catalog = `catalog_version: 1
source:
  repository: open-telemetry/opentelemetry-configuration
  release: v1.2.0
  commit: 3b04ae78576ec407854d734eb4da4f78a67f902c
  file: schema/instrumentation.yaml
definitions:
  general.http.client.request_captured_headers:
    name: general.http.client.request_captured_headers
    description: Configure headers to capture for outbound HTTP requests.
    type: list
    default_behavior: No outbound request headers are captured.
  go.test.option:
    name: go.test.option
    description: Test-only Go-owned catalog entry.
    type: boolean
    deprecated: true
    replaced_by: go.test.option.v2
  go.test.option.v2:
    name: go.test.option.v2
    description: Replacement test-only Go-owned catalog entry.
    type: boolean
`
	if err := os.WriteFile(path, []byte(catalog), filePerm); err != nil {
		t.Fatal(err)
	}
}

func loadTestConfigurationCatalog(t *testing.T, root string) *ConfigurationCatalog {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(DefaultConfigurationCatalogPath))
	catalog, err := LoadConfigurationCatalog(path)
	if err != nil {
		t.Fatalf("LoadConfigurationCatalog() error = %v", err)
	}
	return catalog
}

func TestScanMetadataRepo(t *testing.T) {
	tests := []struct {
		name          string
		goVersion     string
		metaConfig    string
		wantLibs      int
		wantError     bool
		setupOverride func(*testing.T, string)
		assertLib     func(*testing.T, Library)
	}{
		{
			name:      "infers GoMinVersion from go.mod",
			goVersion: "1.25.0",
			metaConfig: `name: test-lib
modules:
  - path: go.opentelemetry.io/otelc/dummy
`,
			wantLibs: 1,
			assertLib: func(t *testing.T, l Library) {
				if l.GoMinVersion != "1.25.0" {
					t.Errorf("GoMinVersion = %q, want 1.25.0", l.GoMinVersion)
				}
			},
		},
		{
			name:      "respects explicitly authored GoMinVersion",
			goVersion: "1.25.0",
			metaConfig: `name: test-lib
go_min_version: "1.20"
modules:
  - path: go.opentelemetry.io/otelc/dummy
`,
			wantLibs: 1,
			assertLib: func(t *testing.T, l Library) {
				if l.GoMinVersion != "1.20" {
					t.Errorf("GoMinVersion = %q, want 1.20 (should not be overwritten)", l.GoMinVersion)
				}
			},
		},
		{
			name:      "multi-module picks highest go version",
			goVersion: "1.20.0", // Ignored because we define two specific modules
			metaConfig: `name: multi-lib
modules:
  - path: go.opentelemetry.io/otelc/mod1
  - path: go.opentelemetry.io/otelc/mod2
`,
			wantLibs: 1,
			setupOverride: func(t *testing.T, root string) {
				writeGoMod(t, root, "instrumentation/mod1", "module go.opentelemetry.io/otelc/mod1\n\ngo 1.20\n")
				writeGoMod(t, root, "instrumentation/mod2", "module go.opentelemetry.io/otelc/mod2\n\ngo 1.25.0\n")
			},
			assertLib: func(t *testing.T, l Library) {
				if l.GoMinVersion != "1.25.0" {
					t.Errorf("GoMinVersion = %q, want 1.25.0", l.GoMinVersion)
				}
				if l.SourcePath == "" {
					t.Errorf("SourcePath was not injected")
				}
			},
		},
		{
			name:      "round-trips new fields and respects authored source_path",
			goVersion: "1.25.0",
			metaConfig: `name: exhaustive-lib
source_path: custom/path
hidden: true
otelc_min_version: v0.10.0
installation:
  methods:
    - automatic
    - wrapper
modules:
  - path: go.opentelemetry.io/otelc/exhaustive
`,
			wantLibs: 1,
			assertLib: func(t *testing.T, l Library) {
				if l.SourcePath != "custom/path" {
					t.Errorf("SourcePath = %q, want custom/path", l.SourcePath)
				}
				if !l.Hidden {
					t.Errorf("Hidden = false, want true")
				}
				if l.OtelcMinVersion != "v0.10.0" {
					t.Errorf("OtelcMinVersion = %q, want v0.10.0", l.OtelcMinVersion)
				}
				if len(l.Installation.Methods) != 2 {
					t.Fatalf("Installation methods len = %d, want 2", len(l.Installation.Methods))
				}
			},
		},
		{
			name:      "malformed metadata yields error",
			goVersion: "1.25.0",
			metaConfig: `name: bad-lib
instalation: # typo should fail strict decoding
  methods: [automatic]
`,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()

			if tt.setupOverride != nil {
				tt.setupOverride(t, root)
			} else {
				writeGoMod(t, root, "instrumentation/dummy", "module go.opentelemetry.io/otelc/dummy\n\ngo "+tt.goVersion+"\n")
			}

			writeMetadata(t, root, "instrumentation/dummy", tt.metaConfig)

			res, err := ScanMetadataRepo(root, nil)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ScanMetadataRepo() error = %v", err)
			}
			if len(res.Libraries) != tt.wantLibs {
				t.Fatalf("Got %d libraries, want %d", len(res.Libraries), tt.wantLibs)
			}
			if tt.wantLibs > 0 {
				tt.assertLib(t, res.Libraries[0])
			}
		})
	}
}

func TestScanMetadataRepoConfigurationEntryShape(t *testing.T) {
	tests := []struct {
		name                string
		config              string
		wantError           string
		wantName            string
		wantType            string
		wantDefault         string
		checkDefault        bool
		wantInline          bool
		wantRef             bool
		wantSchema          bool
		wantRefValue        string
		wantDescription     string
		wantDefaultBehavior string
		wantDeprecated      bool
		wantReplacedBy      string
	}{
		{
			name: "inline declarative configuration",
			config: `- name: go.net_http.enabled
  description: Enables HTTP instrumentation.
  type: boolean
  default: true`,
			wantName:     "go.net_http.enabled",
			wantType:     "boolean",
			wantDefault:  "true",
			checkDefault: true,
			wantInline:   true,
		},
		{
			name: "structured declarative configuration",
			config: `- name: go.http.client.rules
  description: HTTP client rules.
  type: structured_list
  default: "[]"
  schema:
    type: object
    required: [pattern]
    properties:
      pattern:
        type: string
        example: "/users/{id}"
      enabled:
        type: boolean
        default: false
  deprecated: true
  replaced_by: go.http.client.rules.v2`,
			wantInline:     true,
			wantSchema:     true,
			wantName:       "go.http.client.rules",
			wantType:       "structured_list",
			wantDeprecated: true,
			wantReplacedBy: "go.http.client.rules.v2",
		},
		{
			name:                "shared reference resolves and preserves its ref",
			config:              `- ref: general.http.client.request_captured_headers`,
			wantRef:             true,
			wantRefValue:        "general.http.client.request_captured_headers",
			wantName:            "general.http.client.request_captured_headers",
			wantType:            "list",
			wantDescription:     "Configure headers to capture for outbound HTTP requests.",
			wantDefaultBehavior: "No outbound request headers are captured.",
		},
		{
			name:            "Go-owned reference resolves",
			config:          `- ref: go.test.option`,
			wantRef:         true,
			wantRefValue:    "go.test.option",
			wantName:        "go.test.option",
			wantType:        "boolean",
			wantDescription: "Test-only Go-owned catalog entry.",
			wantDeprecated:  true,
			wantReplacedBy:  "go.test.option.v2",
		},
		{
			name: "both selectors",
			config: `- name: go.net_http.enabled
  ref: general.http.client.request_captured_headers`,
			wantError: "exactly one of name or ref",
		},
		{
			name: "shared reference with inline fields",
			config: `- ref: general.http.client.request_captured_headers
  type: list`,
			wantError: `a ref entry must not include inline configuration field "type"`,
		},
		{
			name: "shared reference with deprecation override",
			config: `- ref: general.http.client.request_captured_headers
  deprecated: true`,
			wantError: `a ref entry must not include inline configuration field "deprecated"`,
		},
		{
			name:      "neither selector is rejected",
			config:    `- description: missing selector`,
			wantError: "exactly one of name or ref",
		},
		{
			name: "null name is rejected", config: "- name: null", wantError: "exactly one of name or ref",
		},
		{
			name: "duplicate name is rejected", config: "- name: go.x\n  name: go.y\n  type: boolean", wantError: "already defined",
		},
		{
			name: "old name field is rejected", config: "- name: go.x\n  type: string\n  declarative_name: go.x", wantError: "field declarative_name not found",
		},
		{
			name: "old type field is rejected", config: "- name: go.x\n  type: string\n  declarative_type: string", wantError: "field declarative_type not found",
		},
		{
			name: "old schema field is rejected", config: "- name: go.x\n  type: string\n  declarative_schema: {}", wantError: "field declarative_schema not found",
		},
		{
			name: "invalid boolean default", config: "- name: go.x\n  type: boolean\n  default: yes", wantError: "not a valid boolean",
		},
		{
			name: "invalid list default", config: "- name: go.x\n  type: list\n  default: nope", wantError: "default must encode",
		},
		{
			name: "invalid map default", config: "- name: go.x\n  type: map\n  default: \"[]\"", wantError: "default must encode",
		},
		{
			name: "missing required default property", config: "- name: go.x\n  type: structured_list\n  default: \"[{}]\"\n  schema: {type: object, required: [enabled], properties: {enabled: {type: boolean}}}", wantError: "required schema property",
		},
		{
			name: "wrong typed default property", config: "- name: go.x\n  type: structured_list\n  default: \"[{enabled: nope}]\"\n  schema: {type: object, properties: {enabled: {type: boolean}}}", wantError: "schema property",
		},
		{
			name: "invalid schema property default", config: "- name: go.x\n  type: structured_list\n  schema: {type: object, properties: {enabled: {type: boolean, default: \"false\"}}}", wantError: "default does not match type",
		},
		{
			name: "invalid schema property example", config: "- name: go.x\n  type: structured_list\n  schema: {type: object, properties: {enabled: {type: boolean, example: \"false\"}}}", wantError: "example does not match type",
		},
		{
			name: "non-scalar top-level default is rejected",
			config: `- name: go.net_http.enabled
  type: boolean
  default: [true]`,
			wantError: "cannot unmarshal !!seq into string",
		},
		{
			name:      "null ref selector is rejected",
			config:    `- ref: null`,
			wantError: "exactly one of name or ref",
		},
		{
			name:      "empty name",
			config:    `- name: ""`,
			wantError: "exactly one of name or ref",
		},
		{
			name:      "empty ref",
			config:    `- ref: ""`,
			wantError: "ref must not be empty",
		},
		{
			name:      "missing type",
			config:    `- name: go.test`,
			wantError: "unsupported or missing type",
		},
		{
			name:      "name with surrounding whitespace",
			config:    `- name: " go.net_http.enabled "`,
			wantError: "name must not have leading or trailing whitespace",
		},
		{
			name: "general path omits development suffix",
			config: `- name: general.http.client.request_captured_headers/development
  type: list`,
			wantError: "name must be a canonical general. or go. configuration path",
		},
		{
			name:      "ref with surrounding whitespace",
			config:    `- ref: " general.http.client.request_captured_headers "`,
			wantError: "ref must not have leading or trailing whitespace",
		},
		{
			name:      "ref must use a canonical namespace",
			config:    `- ref: thirdparty.option`,
			wantError: "ref must be a canonical general. or go. configuration path",
		},
		{
			name: "empty replacement is rejected",
			config: `- name: go.test.old
  type: boolean
  deprecated: true
  replaced_by: ""`,
			wantError: "replaced_by must not be empty",
		},
		{
			name: "replacement whitespace is rejected",
			config: `- name: go.test.old
  type: boolean
  deprecated: true
  replaced_by: " go.test.new "`,
			wantError: "replaced_by must not have leading or trailing whitespace",
		},
		{
			name: "replacement must be a string",
			config: `- name: go.test.old
  type: boolean
  deprecated: true
  replaced_by: {invalid: mapping}`,
			wantError: "cannot unmarshal !!map into string",
		},
		{
			name: "replacement requires deprecation",
			config: `- name: go.test.old
  type: boolean
  replaced_by: go.test.new`,
			wantError: "replaced_by requires deprecated to be true",
		},
		{
			name: "unknown type",
			config: `- name: go.net_http.enabled
  type: str`,
			wantError: "unsupported or missing type \"str\"",
		},
		{
			name: "schema on scalar declarative type",
			config: `- name: go.net_http.enabled
  type: boolean
  schema:
    type: object
    properties: {}`,
			wantError: "schema requires type structured_list",
		},
		{
			name: "structured list requires schema",
			config: `- name: go.http.client.rules
  type: structured_list`,
			wantError: "structured_list requires schema",
		},
		{
			name: "schema must describe an object",
			config: `- name: go.http.client.rules
  type: structured_list
  schema:
    type: string
    properties: {}`,
			wantError: "schema type must be object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			catalogRoot := t.TempDir()
			writeGoMod(t, root, "instrumentation/dummy", "module go.opentelemetry.io/otelc/dummy\n\ngo 1.25.0\n")
			writeConfigurationCatalog(t, catalogRoot)
			catalog := loadTestConfigurationCatalog(t, catalogRoot)
			writeMetadata(t, root, "instrumentation/dummy", "name: test-lib\nconfigurations:\n"+tt.config+"\n")

			result, err := ScanMetadataRepo(root, catalog)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("ScanMetadataRepo() error = %v, want error containing %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("ScanMetadataRepo() error = %v", err)
			}
			if len(result.Libraries) != 1 {
				t.Fatalf("libraries = %d, want 1", len(result.Libraries))
			}
			configurations := result.Libraries[0].Configurations
			if len(configurations) != 1 {
				t.Fatalf("configurations = %d, want 1", len(configurations))
			}
			configuration := configurations[0]
			if configuration.Name != tt.wantName {
				t.Errorf("name = %q, want %q", configuration.Name, tt.wantName)
			}
			if got := configuration.Ref != nil; got != tt.wantRef {
				t.Errorf("ref present = %v, want %v", got, tt.wantRef)
			}
			if tt.wantRefValue != "" && (configuration.Ref == nil || *configuration.Ref != tt.wantRefValue) {
				t.Errorf("ref = %v, want %q", configuration.Ref, tt.wantRefValue)
			}
			if configuration.Type != tt.wantType {
				t.Errorf("type = %q, want %q", configuration.Type, tt.wantType)
			}
			if tt.wantDescription != "" && configuration.Description != tt.wantDescription {
				t.Errorf("description = %q, want %q", configuration.Description, tt.wantDescription)
			}
			if configuration.DefaultBehavior != tt.wantDefaultBehavior {
				t.Errorf("default_behavior = %q, want %q", configuration.DefaultBehavior, tt.wantDefaultBehavior)
			}
			if configuration.Deprecated != tt.wantDeprecated {
				t.Errorf("deprecated = %v, want %v", configuration.Deprecated, tt.wantDeprecated)
			}
			if got := configuration.ReplacedBy; (got == nil) != (tt.wantReplacedBy == "") ||
				(got != nil && *got != tt.wantReplacedBy) {
				t.Errorf("replaced_by = %v, want %q", got, tt.wantReplacedBy)
			}
			if tt.wantInline {
				if tt.checkDefault && (configuration.Default == nil || *configuration.Default != tt.wantDefault) {
					t.Errorf("default = %v, want string %q", configuration.Default, tt.wantDefault)
				}
				if tt.wantSchema {
					if configuration.Schema == nil {
						t.Fatal("schema = nil, want schema")
					}
					if got := configuration.Schema.Properties["enabled"].Default; got != false {
						t.Errorf("schema property default = %#v, want false bool", got)
					}
				}
			}
			serialized, err := yaml.Marshal(result.Libraries[0])
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			var roundTripped Library
			if err := yaml.Unmarshal(serialized, &roundTripped); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if !reflect.DeepEqual(roundTripped.Configurations, configurations) {
				t.Errorf("round-tripped configurations = %#v, want %#v", roundTripped.Configurations, configurations)
			}
		})
	}
}

func TestScanMetadataRepoUnknownConfigurationRef(t *testing.T) {
	root := t.TempDir()
	catalogRoot := t.TempDir()
	writeGoMod(t, root, "instrumentation/dummy", "module go.opentelemetry.io/otelc/dummy\n\ngo 1.25.0\n")
	writeConfigurationCatalog(t, catalogRoot)
	catalog := loadTestConfigurationCatalog(t, catalogRoot)
	writeMetadata(t, root, "instrumentation/dummy", `name: test-lib
configurations:
  - ref: general.unknown
`)

	_, err := ScanMetadataRepo(root, catalog)
	if err == nil || !strings.Contains(err.Error(), `unknown configuration ref "general.unknown"`) {
		t.Fatalf("ScanMetadataRepo() error = %v, want unknown ref diagnostic", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(root, "instrumentation", "dummy", "metadata.yaml")) {
		t.Errorf("ScanMetadataRepo() error = %v, want metadata path", err)
	}
}

func TestScanMetadataRepoConfigurationRefRequiresCatalog(t *testing.T) {
	root := t.TempDir()
	writeGoMod(t, root, "instrumentation/dummy", "module go.opentelemetry.io/otelc/dummy\n\ngo 1.25.0\n")
	writeMetadata(t, root, "instrumentation/dummy", `name: test-lib
configurations:
  - ref: general.http.client.request_captured_headers
`)

	_, err := ScanMetadataRepo(root, nil)
	if err == nil || !strings.Contains(err.Error(), "requires a centrally loaded catalog") {
		t.Fatalf("ScanMetadataRepo() error = %v, want central-catalog diagnostic", err)
	}
}

func TestScanMetadataRepoInlineConfigurationWithoutCatalog(t *testing.T) {
	root := t.TempDir()
	writeGoMod(t, root, "instrumentation/dummy", "module example.com/hooks\n\ngo 1.26.0\n")
	writeMetadata(t, root, "instrumentation/dummy", "name: test-lib\nconfigurations:\n  - name: go.test.enabled\n    type: boolean\n    default: false\n")
	result, err := ScanMetadataRepo(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Libraries) != 1 || len(result.Libraries[0].Configurations) != 1 || result.Libraries[0].Configurations[0].Name != "go.test.enabled" {
		t.Fatalf("inline metadata must not require a catalog: %#v", result)
	}
}

func TestScanRepo(t *testing.T) {
	const contrib = "go.opentelemetry.io/contrib/"

	tests := []struct {
		name      string
		setup     func(t *testing.T) string // builds the repo tree, returns its root
		wantErr   bool
		wantNames []string // expected library names, in returned (sorted) order
	}{
		{
			name: "fuses contrib modules from both subtrees, sorted, non-contrib dropped",
			setup: func(t *testing.T) string {
				root := t.TempDir()
				writeGoMod(t, root, "instrumentation/net/http/otelhttp", modFile(contrib+"instrumentation/net/http/otelhttp"))
				writeGoMod(t, root, "instrumentation/example.com/other", modFile("example.com/other")) // non-contrib, dropped
				writeGoMod(t, root, "bridges/otelslog", modFile(contrib+"bridges/otelslog"))
				return root
			},
			wantNames: []string{"bridges-otelslog", "instrumentation-net-http-otelhttp"},
		},
		{
			name:    "missing subtree fails hard rather than shipping an empty inventory",
			setup:   func(t *testing.T) string { return t.TempDir() }, // no instrumentation/ or bridges/
			wantErr: true,
		},
		{
			name: "malformed go.mod fails hard rather than dropping a library",
			setup: func(t *testing.T) string {
				root := t.TempDir()
				// bridges/ is valid, so a parse error is the only possible failure.
				writeGoMod(t, root, "bridges/otelslog", modFile(contrib+"bridges/otelslog"))
				writeGoMod(t, root, "instrumentation/broken", "module \"unterminated\n")
				return root
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := ScanRepo(tt.setup(t))
			if tt.wantErr {
				if err == nil {
					t.Fatal("ScanRepo() error = nil, want error")
				}
				if res != nil {
					t.Errorf("ScanRepo() result = %+v, want nil on error", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("ScanRepo() error = %v", err)
			}
			got := make([]string, len(res.Libraries))
			for i, lib := range res.Libraries {
				got[i] = lib.Name
			}
			if !slices.Equal(got, tt.wantNames) {
				t.Errorf("ScanRepo() names = %v, want %v", got, tt.wantNames)
			}
		})
	}
}
