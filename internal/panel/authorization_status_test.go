package panel

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

func TestOverviewAuthorizationStatusIsNonSecretAndDeterministic(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name, access string
		expires      int64
		want         string
	}{
		{"valid", "token", now.Add(time.Second).Unix(), "valid"},
		{"boundary", "token", now.Unix(), "expired"},
		{"unknown", "token", 0, "unknown"},
		{"missing", "  ", now.Add(time.Hour).Unix(), "missing"},
	}
	for _, tc := range cases {
		a := &auth.Auth{AccessToken: tc.access, ExpiresAt: tc.expires}
		got := a.CredentialStatus(now)
		if got.State != tc.want || got.ExpiresAt != tc.expires {
			t.Errorf("%s: got=%+v want state=%s expires=%d", tc.name, got, tc.want, tc.expires)
		}
		b, _ := json.Marshal(got)
		if string(b) == "" || string(b) == "token" || string(b) == "\"token\"" {
			t.Errorf("%s: status leaked credential: %s", tc.name, b)
		}
	}
}

func TestCredentialStatusIgnoresSourceHintAndPoolState(t *testing.T) {
	p := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "token", ExpiresAt: 1_700_000_100, FilePath: t.TempDir() + "/a.json"}
	p.Add(a)
	p.Disable("u1", "test")
	st := a.CredentialStatus(time.Unix(1_700_000_000, 0))
	if st.State != "valid" {
		t.Fatalf("disabled account misclassified: %+v", st)
	}
}

func TestOverviewIncludesOnlyNonSecretAuthorizationMetadata(t *testing.T) {
	accounts := pool.New("")
	token := "fixture-distinct-access-secret"
	refresh := "fixture-distinct-refresh-secret"
	a := &auth.Auth{UID: "status-fixture", AccessToken: token, RefreshToken: refresh, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	accounts.Add(a)
	accounts.Disable(a.UID, "manual disable")
	accounts.Pause(a.UID)
	accounts.Cooldown(a.UID, pool.CoolSoft, time.Minute, "fixture rate limit")
	p := New(Config{Pool: accounts})
	rec := httptest.NewRecorder()
	p.overview(rec, httptest.NewRequest("GET", "/", nil))
	var d struct {
		Accounts []struct {
			Authorization auth.CredentialStatus `json:"authorization"`
		}
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &d) != nil || len(d.Accounts) != 1 || d.Accounts[0].Authorization.State != "valid" || d.Accounts[0].Authorization.ExpiresAt != a.ExpiresAt {
		t.Fatalf("bad status metadata: %s", rec.Body)
	}
	for _, secret := range []string{token, refresh, "accessToken", "refreshToken"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatal("overview leaked credential")
		}
	}
}
