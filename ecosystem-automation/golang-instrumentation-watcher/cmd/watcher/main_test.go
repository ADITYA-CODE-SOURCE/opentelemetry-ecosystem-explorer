// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

func TestParseTargetsConfigCentralCatalog(t *testing.T) {
	config, err := parseTargetsConfig([]byte(`configuration_catalog:
  url: https://github.com/open-telemetry/opentelemetry-go-compile-instrumentation.git
  ref: main
  path: tool/internal/configuration/catalog.yaml
targets:
  - url: https://example.com/instrumentation.git
    strategy: metadata
`))
	if err != nil {
		t.Fatalf("parseTargetsConfig() error = %v", err)
	}
	if config.ConfigurationCatalog == nil {
		t.Fatal("configuration_catalog = nil, want central catalog source")
	}
	if config.ConfigurationCatalog.Ref != "main" {
		t.Errorf("catalog ref = %q, want main", config.ConfigurationCatalog.Ref)
	}
	if config.ConfigurationCatalog.Path != "tool/internal/configuration/catalog.yaml" {
		t.Errorf("catalog path = %q, want configured path", config.ConfigurationCatalog.Path)
	}
}

func TestParseTargetsConfigWithoutCentralCatalog(t *testing.T) {
	config, err := parseTargetsConfig([]byte(`targets:
  - url: https://example.com/instrumentation.git
    strategy: metadata
`))
	if err != nil {
		t.Fatalf("parseTargetsConfig() error = %v", err)
	}
	if config.ConfigurationCatalog != nil {
		t.Errorf("configuration_catalog = %+v, want nil", config.ConfigurationCatalog)
	}
}

func TestParseTargetsConfigRejectsInvalidCentralCatalog(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "missing ref",
			data: "configuration_catalog:\n  url: https://example.com/otelc.git\n  path: catalog.yaml\n",
			want: "configuration_catalog.ref must be a non-empty ref",
		},
		{
			name: "path escapes checkout",
			data: "configuration_catalog:\n  url: https://example.com/otelc.git\n  ref: main\n  path: ../catalog.yaml\n",
			want: "must stay within the catalog checkout",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTargetsConfig([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseTargetsConfig() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}
