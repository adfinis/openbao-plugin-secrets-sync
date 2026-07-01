package backend

import (
	"strings"
	"testing"
)

func FuzzRenderAssociationObjectName(f *testing.F) {
	f.Add("{{ path }}/{{ key }}", "app/db", "fake", "default", "password")
	f.Add("{{ destination.type }}/{{ destination.name }}/{{ key }}", "app/db", "gitlab", "prod", "APP_PASSWORD")
	f.Add("static-name", "app/db", "aws-sm", "prod", "secret-path")

	f.Fuzz(func(t *testing.T, template string, path string, destinationType string, destinationName string, key string) {
		if len(template) > 512 || len(path) > 256 || len(destinationType) > 64 ||
			len(destinationName) > 128 || len(key) > 256 {
			t.Skip("input outside fuzz smoke size")
		}
		rendered, err := renderAssociationObjectName(template, path, destinationType, destinationName, key)
		if err != nil {
			return
		}
		if rendered == "" {
			t.Fatal("rendered name must not be empty")
		}
		if strings.HasPrefix(rendered, "/") || strings.HasSuffix(rendered, "/") {
			t.Fatalf("rendered name = %q, want trimmed path", rendered)
		}
		if strings.Contains(rendered, "{{") || strings.Contains(rendered, "}}") {
			t.Fatalf("rendered name contains unresolved template marker: %q", rendered)
		}
	})
}
