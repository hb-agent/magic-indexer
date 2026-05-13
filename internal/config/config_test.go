package config

import (
	"os"
	"testing"
)

func TestGetEnv(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		defaultValue string
		envValue     string
		setEnv       bool
		want         string
	}{
		{
			name:         "returns default when env not set",
			key:          "TEST_CONFIG_UNSET",
			defaultValue: "default_value",
			setEnv:       false,
			want:         "default_value",
		},
		{
			name:         "returns env value when set",
			key:          "TEST_CONFIG_SET",
			defaultValue: "default_value",
			envValue:     "env_value",
			setEnv:       true,
			want:         "env_value",
		},
		{
			name:         "returns default when env is empty",
			key:          "TEST_CONFIG_EMPTY",
			defaultValue: "default_value",
			envValue:     "",
			setEnv:       true,
			want:         "default_value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv(tt.key)
			if tt.setEnv {
				os.Setenv(tt.key, tt.envValue)
				defer os.Unsetenv(tt.key)
			}

			got := getEnv(tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("getEnv(%q, %q) = %q, want %q", tt.key, tt.defaultValue, got, tt.want)
			}
		})
	}
}

func TestGetEnvInt(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		defaultValue int
		envValue     string
		setEnv       bool
		want         int
	}{
		{
			name:         "returns default when env not set",
			key:          "TEST_CONFIG_INT_UNSET",
			defaultValue: 42,
			setEnv:       false,
			want:         42,
		},
		{
			name:         "returns parsed int when set",
			key:          "TEST_CONFIG_INT_SET",
			defaultValue: 42,
			envValue:     "100",
			setEnv:       true,
			want:         100,
		},
		{
			name:         "returns default when env is invalid int",
			key:          "TEST_CONFIG_INT_INVALID",
			defaultValue: 42,
			envValue:     "not_a_number",
			setEnv:       true,
			want:         42,
		},
		{
			name:         "returns default when env is empty",
			key:          "TEST_CONFIG_INT_EMPTY",
			defaultValue: 42,
			envValue:     "",
			setEnv:       true,
			want:         42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv(tt.key)
			if tt.setEnv {
				os.Setenv(tt.key, tt.envValue)
				defer os.Unsetenv(tt.key)
			}

			got := getEnvInt(tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("getEnvInt(%q, %d) = %d, want %d", tt.key, tt.defaultValue, got, tt.want)
			}
		})
	}
}

func TestGetEnvBool(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		defaultValue bool
		envValue     string
		setEnv       bool
		want         bool
	}{
		{
			name:         "returns default when env not set",
			key:          "TEST_CONFIG_BOOL_UNSET",
			defaultValue: false,
			setEnv:       false,
			want:         false,
		},
		{
			name:         "returns true for 'true'",
			key:          "TEST_CONFIG_BOOL_TRUE",
			defaultValue: false,
			envValue:     "true",
			setEnv:       true,
			want:         true,
		},
		{
			name:         "returns true for 'TRUE'",
			key:          "TEST_CONFIG_BOOL_TRUE_UPPER",
			defaultValue: false,
			envValue:     "TRUE",
			setEnv:       true,
			want:         true,
		},
		{
			name:         "returns true for '1'",
			key:          "TEST_CONFIG_BOOL_ONE",
			defaultValue: false,
			envValue:     "1",
			setEnv:       true,
			want:         true,
		},
		{
			name:         "returns true for 'yes'",
			key:          "TEST_CONFIG_BOOL_YES",
			defaultValue: false,
			envValue:     "yes",
			setEnv:       true,
			want:         true,
		},
		{
			name:         "returns false for 'false'",
			key:          "TEST_CONFIG_BOOL_FALSE",
			defaultValue: true,
			envValue:     "false",
			setEnv:       true,
			want:         false,
		},
		{
			name:         "returns false for '0'",
			key:          "TEST_CONFIG_BOOL_ZERO",
			defaultValue: true,
			envValue:     "0",
			setEnv:       true,
			want:         false,
		},
		{
			name:         "returns false for invalid value",
			key:          "TEST_CONFIG_BOOL_INVALID",
			defaultValue: true,
			envValue:     "invalid",
			setEnv:       true,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv(tt.key)
			if tt.setEnv {
				os.Setenv(tt.key, tt.envValue)
				defer os.Unsetenv(tt.key)
			}

			got := getEnvBool(tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("getEnvBool(%q, %v) = %v, want %v", tt.key, tt.defaultValue, got, tt.want)
			}
		})
	}
}

func TestRedactPassword(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "postgres URL with password",
			input: "postgres://user:secretpass@localhost:5432/dbname",
			want:  "postgres://user:***@localhost:5432/dbname",
		},
		{
			name:  "postgresql URL with password",
			input: "postgresql://admin:mypassword@db.example.com:5432/production",
			want:  "postgresql://admin:***@db.example.com:5432/production",
		},
		{
			name:  "URL without password",
			input: "sqlite:data/hypergoat.db",
			want:  "sqlite:data/hypergoat.db",
		},
		{
			name:  "URL with @ but no password",
			input: "user@host",
			want:  "user@host",
		},
		{
			name:  "empty URL",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactPassword(tt.input)
			if got != tt.want {
				t.Errorf("RedactPassword(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	// validBudgets is the baseline pair every passing test case carries
	// so the new issue-71 Layer-1/Layer-2 ordering rules don't reject
	// otherwise-valid fixtures by accident.
	validBudgets := func(c Config) Config {
		if c.DBStatementTimeoutMs == 0 {
			c.DBStatementTimeoutMs = 30000
		}
		if c.GraphQLPublicQueryTimeoutMs == 0 {
			c.GraphQLPublicQueryTimeoutMs = 5000
		}
		return c
	}
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: validBudgets(Config{
				SecretKeyBase:            "this_is_a_very_long_secret_key_that_is_definitely_more_than_64_characters_long_for_testing",
				Port:                     8080,
				OAuthLegacyDPoPJKTCutoff: 1744416000,
			}),
			wantErr: false,
		},
		{
			name: "secret key too short",
			config: validBudgets(Config{
				SecretKeyBase:            "short_key",
				Port:                     8080,
				OAuthLegacyDPoPJKTCutoff: 1744416000,
			}),
			wantErr: true,
		},
		{
			name: "port too low",
			config: validBudgets(Config{
				SecretKeyBase:            "this_is_a_very_long_secret_key_that_is_definitely_more_than_64_characters_long_for_testing",
				Port:                     0,
				OAuthLegacyDPoPJKTCutoff: 1744416000,
			}),
			wantErr: true,
		},
		{
			name: "port too high",
			config: validBudgets(Config{
				SecretKeyBase:            "this_is_a_very_long_secret_key_that_is_definitely_more_than_64_characters_long_for_testing",
				Port:                     70000,
				OAuthLegacyDPoPJKTCutoff: 1744416000,
			}),
			wantErr: true,
		},
		{
			name: "dev placeholder secret rejected",
			config: validBudgets(Config{
				SecretKeyBase:            devSecretKeyBase,
				Port:                     8080,
				OAuthLegacyDPoPJKTCutoff: 1744416000,
			}),
			wantErr: true,
		},
		{
			name: "missing OAUTH_LEGACY_DPOP_JKT_CUTOFF rejected (fail-closed)",
			config: validBudgets(Config{
				SecretKeyBase: "this_is_a_very_long_secret_key_that_is_definitely_more_than_64_characters_long_for_testing",
				Port:          8080,
			}),
			wantErr: true,
		},
		{
			name: "negative OAUTH_LEGACY_DPOP_JKT_CUTOFF rejected",
			config: validBudgets(Config{
				SecretKeyBase:            "this_is_a_very_long_secret_key_that_is_definitely_more_than_64_characters_long_for_testing",
				Port:                     8080,
				OAuthLegacyDPoPJKTCutoff: -1,
			}),
			wantErr: true,
		},
		{
			name: "issue #71: DB_STATEMENT_TIMEOUT_MS too low",
			config: Config{
				SecretKeyBase:               "this_is_a_very_long_secret_key_that_is_definitely_more_than_64_characters_long_for_testing",
				Port:                        8080,
				OAuthLegacyDPoPJKTCutoff:    1744416000,
				DBStatementTimeoutMs:        500,
				GraphQLPublicQueryTimeoutMs: 100,
			},
			wantErr: true,
		},
		{
			name: "issue #71: GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS too low",
			config: Config{
				SecretKeyBase:               "this_is_a_very_long_secret_key_that_is_definitely_more_than_64_characters_long_for_testing",
				Port:                        8080,
				OAuthLegacyDPoPJKTCutoff:    1744416000,
				DBStatementTimeoutMs:        30000,
				GraphQLPublicQueryTimeoutMs: 50,
			},
			wantErr: true,
		},
		{
			name: "issue #71: Layer 1 <= Layer 2 rejected",
			config: Config{
				SecretKeyBase:               "this_is_a_very_long_secret_key_that_is_definitely_more_than_64_characters_long_for_testing",
				Port:                        8080,
				OAuthLegacyDPoPJKTCutoff:    1744416000,
				DBStatementTimeoutMs:        5000,
				GraphQLPublicQueryTimeoutMs: 5000,
			},
			wantErr: true,
		},
		{
			name: "issue #71: Layer 1 only slightly > Layer 2 accepted",
			config: Config{
				SecretKeyBase:               "this_is_a_very_long_secret_key_that_is_definitely_more_than_64_characters_long_for_testing",
				Port:                        8080,
				OAuthLegacyDPoPJKTCutoff:    1744416000,
				DBStatementTimeoutMs:        5001,
				GraphQLPublicQueryTimeoutMs: 5000,
			},
			wantErr: false,
		},
		{
			name: "issue #71: Layer 2 exceeding chi outer timeout rejected",
			config: Config{
				SecretKeyBase:               "this_is_a_very_long_secret_key_that_is_definitely_more_than_64_characters_long_for_testing",
				Port:                        8080,
				OAuthLegacyDPoPJKTCutoff:    1744416000,
				DBStatementTimeoutMs:        120000,
				GraphQLPublicQueryTimeoutMs: HTTPRouterTimeoutMs, // == outer ceiling, must be rejected
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfigAddress(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{
			name:   "default host and port",
			config: Config{Host: "127.0.0.1", Port: 8080},
			want:   "127.0.0.1:8080",
		},
		{
			name:   "custom host and port",
			config: Config{Host: "0.0.0.0", Port: 3000},
			want:   "0.0.0.0:3000",
		},
		{
			name:   "localhost",
			config: Config{Host: "localhost", Port: 443},
			want:   "localhost:443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.Address()
			if got != tt.want {
				t.Errorf("Config.Address() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateRandomKey(t *testing.T) {
	tests := []struct {
		name   string
		length int
	}{
		{name: "32 bytes", length: 32},
		{name: "64 bytes", length: 64},
		{name: "128 bytes", length: 128},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := generateRandomKey(tt.length)
			if err != nil {
				t.Errorf("generateRandomKey(%d) error = %v", tt.length, err)
				return
			}
			if len(key) != tt.length {
				t.Errorf("generateRandomKey(%d) returned key of length %d", tt.length, len(key))
			}
		})
	}

	// Test that generated keys are unique
	t.Run("keys are unique", func(t *testing.T) {
		key1, _ := generateRandomKey(64)
		key2, _ := generateRandomKey(64)
		if key1 == key2 {
			t.Error("generateRandomKey() returned same key twice")
		}
	})
}

func TestNormalizeExternalBaseURL(t *testing.T) {
	const (
		host = "127.0.0.1"
		port = 8080
	)
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "empty falls back to host:port",
			raw:  "",
			want: "http://127.0.0.1:8080",
		},
		{
			name: "whitespace-only falls back to host:port",
			raw:  "   ",
			want: "http://127.0.0.1:8080",
		},
		{
			name: "schemeless public host gets https://",
			raw:  "magic-indexer.example.com",
			want: "https://magic-indexer.example.com",
		},
		{
			name: "schemeless localhost gets http://",
			raw:  "localhost:8080",
			want: "http://localhost:8080",
		},
		{
			name: "schemeless 127.0.0.1 gets http://",
			raw:  "127.0.0.1:8080",
			want: "http://127.0.0.1:8080",
		},
		{
			name: "schemeless [::1] gets http://",
			raw:  "[::1]:8080",
			want: "http://[::1]:8080",
		},
		{
			name: "schemeless mixed-case localhost gets http://",
			raw:  "LocalHost:8080",
			want: "http://LocalHost:8080",
		},
		{
			name: "lowercases uppercase scheme",
			raw:  "HTTPS://Magic-Indexer.example.com",
			want: "https://Magic-Indexer.example.com",
		},
		{
			name: "preserves host case once scheme is lowercased",
			raw:  "Https://CamelCase.Example.COM",
			want: "https://CamelCase.Example.COM",
		},
		{
			name: "trims surrounding whitespace",
			raw:  "  https://magic-indexer.example.com  ",
			want: "https://magic-indexer.example.com",
		},
		{
			name: "trims trailing slash",
			raw:  "https://magic-indexer.example.com/",
			want: "https://magic-indexer.example.com",
		},
		{
			name: "trims multiple trailing slashes",
			raw:  "https://magic-indexer.example.com///",
			want: "https://magic-indexer.example.com",
		},
		{
			name: "preserves path",
			raw:  "https://magic-indexer.example.com/api",
			want: "https://magic-indexer.example.com/api",
		},
		{
			name: "schemeless with path defaults to https",
			raw:  "magic-indexer.example.com/api",
			want: "https://magic-indexer.example.com/api",
		},
		{
			name: "preserves http scheme",
			raw:  "http://internal.example.com",
			want: "http://internal.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeExternalBaseURL(tt.raw, host, port)
			if got != tt.want {
				t.Errorf("normalizeExternalBaseURL(%q, %q, %d) = %q, want %q",
					tt.raw, host, port, got, tt.want)
			}
		})
	}
}
