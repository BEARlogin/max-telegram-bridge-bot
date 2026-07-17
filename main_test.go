package main

import (
	"os"
	"testing"
)

func TestEnvOr(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		envVal   string
		fallback string
		expected string
	}{
		{"env set", "TEST_ENVOR_SET", "from_env", "default", "from_env"},
		{"env empty", "TEST_ENVOR_EMPTY", "", "default", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envVal != "" {
				os.Setenv(tt.key, tt.envVal)
				defer os.Unsetenv(tt.key)
			} else {
				os.Unsetenv(tt.key)
			}
			got := envOr(tt.key, tt.fallback)
			if got != tt.expected {
				t.Errorf("envOr(%q, %q) = %q, want %q", tt.key, tt.fallback, got, tt.expected)
			}
		})
	}
}

func TestGenKey(t *testing.T) {
	key := genKey()
	if len(key) != 16 {
		t.Errorf("genKey() length = %d, want 16", len(key))
	}

	// Keys should be unique
	key2 := genKey()
	if key == key2 {
		t.Errorf("genKey() returned same key twice: %s", key)
	}

	// Should be valid hex
	for _, c := range key {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("genKey() contains non-hex char: %c in %s", c, key)
		}
	}
}
