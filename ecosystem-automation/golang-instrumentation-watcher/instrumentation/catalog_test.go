package instrumentation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurationCatalogValidation(t *testing.T) {
	root := t.TempDir()
	writeConfigurationCatalog(t, root)
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(configurationCatalogPath)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, from, to, want string }{
		{"version", "catalog_version: 1", "catalog_version: 2", "unsupported configuration catalog version"},
		{"name mismatch", "name: go.test.option", "name: go.test.other", "key must equal name"},
		{"unknown type", "type: boolean", "type: str", "unsupported type"},
		{"nested ref", "name: go.test.option", "ref: go.test.option", "must not include inline"},
		{"old field", "name: go.test.option", "declarative_name: go.test.option", "exactly one of name or ref"},
		{"missing schema", "type: boolean", "type: structured_list", "structured_list requires schema"},
		{"invalid property default", "type: boolean", "type: structured_list\n    schema: {type: object, properties: {enabled: {type: boolean, default: \"false\"}}}", "default does not match type"},
		{"invalid property example", "type: boolean", "type: structured_list\n    schema: {type: object, properties: {enabled: {type: boolean, example: null}}}", "example does not match type"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseConfigurationCatalog([]byte(strings.Replace(string(data), tt.from, tt.to, 1)), "catalog.yaml")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("catalog error = %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := parseConfigurationCatalog(append(data, []byte("\n---\n{}\n")...), "catalog.yaml"); err == nil {
		t.Fatal("multiple catalog documents must not be accepted")
	}
}
