package risk

import (
	"testing"
)

func TestSensitiveEntries(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  []Entry
	}{
		{
			name:  "env file",
			paths: []string{".env", "src/app.go"},
			want:  []Entry{{Path: ".env", Kind: KindEnv}},
		},
		{
			name:  "env variant and key",
			paths: []string{"config/.env.production", "deploy/server.key"},
			want: []Entry{
				{Path: "config/.env.production", Kind: KindEnv},
				{Path: "deploy/server.key", Kind: KindSecrets},
			},
		},
		{
			name:  "private key basename",
			paths: []string{"id_rsa", ".ssh/id_ed25519"},
			want: []Entry{
				{Path: ".ssh/id_ed25519", Kind: KindSecrets},
				{Path: "id_rsa", Kind: KindSecrets},
			},
		},
		{
			name:  "secrets dir trumps extension",
			paths: []string{"secrets/service.toml", "secrets/env.token"},
			want: []Entry{
				{Path: "secrets/env.token", Kind: KindSecrets},
				{Path: "secrets/service.toml", Kind: KindSecrets},
			},
		},
		{
			name:  "credentials",
			paths: []string{"credentials.json", ".aws/config"},
			want: []Entry{
				{Path: ".aws/config", Kind: KindCredentials},
				{Path: "credentials.json", Kind: KindCredentials},
			},
		},
		{
			name:  "clean paths ignored",
			paths: []string{"src/app.go", "tests/app_test.go", "go.mod", "README.md"},
			want:  nil,
		},
		{
			name:  "docker config",
			paths: []string{".docker/config.json", ".docker/config.json.bak"},
			want: []Entry{
				{Path: ".docker/config.json", Kind: KindCredentials},
				{Path: ".docker/config.json.bak", Kind: KindCredentials},
			},
		},
		{
			name:  "dedup and sort",
			paths: []string{"z.key", ".env", "z.key"},
			want: []Entry{
				{Path: ".env", Kind: KindEnv},
				{Path: "z.key", Kind: KindSecrets},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Entries(tc.paths)
			if len(got) != len(tc.want) {
				t.Fatalf("Entries(%v) = %+v, ожидалось %+v", tc.paths, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Entries(%v) = %+v, ожидалось %+v", tc.paths, got, tc.want)
				}
			}
		})
	}
}

func TestSensitiveEntryDeterminism(t *testing.T) {
	base := []string{"src/a.go", "secrets/x.toml", ".env", "m.key", "go.mod"}
	first := Entries(base)
	second := Entries(append([]string(nil), base...))
	if len(first) != len(second) {
		t.Fatalf("детерминизм нарушен: %+v vs %+v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("детерминизм нарушен: %+v vs %+v", first, second)
		}
	}
}
