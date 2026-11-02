package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTokenFixture(t *testing.T, value map[string]any, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token.json")
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func validTokenFixture() map[string]any {
	return map[string]any{
		"access_token": "fixture-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"scope":        "openid user.credit.exchange",
		"obtained_at":  time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	}
}

func TestReadTokenFileValidAndMetadataFailures(t *testing.T) {
	valid := writeTokenFixture(t, validTokenFixture(), 0600)
	if got, err := readTokenFile(valid); err != nil || got != "fixture-token" {
		t.Fatalf("valid token got=%q err=%v", got, err)
	}
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"expired", func(v map[string]any) {
			v["obtained_at"] = time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
			v["expires_in"] = 1
		}},
		{"future", func(v map[string]any) { v["obtained_at"] = time.Now().UTC().Add(time.Minute).Format(time.RFC3339) }},
		{"missing-obtained-at", func(v map[string]any) { delete(v, "obtained_at") }},
		{"wrong-type", func(v map[string]any) { v["token_type"] = "Basic" }},
		{"missing-scope", func(v map[string]any) { v["scope"] = "openid" }},
		{"missing-expiry", func(v map[string]any) { delete(v, "expires_in") }},
		{"nonpositive-expiry", func(v map[string]any) { v["expires_in"] = 0 }},
		{"overflow-expiry", func(v map[string]any) { v["expires_in"] = "9223372036854775808" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := validTokenFixture()
			tc.edit(v)
			if _, err := readTokenFile(writeTokenFixture(t, v, 0600)); err == nil {
				t.Fatal("invalid token metadata accepted")
			}
		})
	}
}

func TestReadTokenFileRejectsSymlinkAndPermissiveFile(t *testing.T) {
	dir := t.TempDir()
	target := writeTokenFixture(t, validTokenFixture(), 0600)
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readTokenFile(link); err == nil {
		t.Fatal("symlink accepted")
	}
	permissive := writeTokenFixture(t, validTokenFixture(), 0644)
	if _, err := readTokenFile(permissive); err == nil || !strings.Contains(err.Error(), "group/world") {
		t.Fatalf("permissive file err=%v", err)
	}
}
