package panel

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestActivitiesEndpointRequiresAuth(t *testing.T) {
	p := newTestPanel()
	r := httptest.NewRecorder()
	p.ServeHTTP(r, httptest.NewRequest("GET", "/panel/api/activities", nil))
	if r.Code != 401 {
		t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
	}
}

func TestActivitiesEndpointIsReadOnlyCatalog(t *testing.T) {
	p := newTestPanel()
	req := httptest.NewRequest("GET", "/panel/api/activities", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	r := httptest.NewRecorder()
	p.ServeHTTP(r, req)
	if r.Code != 200 {
		t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
	}
	var body struct {
		VerifiedAt string           `json:"verified_at"`
		Activities []map[string]any `json:"activities"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.VerifiedAt != "2026-11-03" || len(body.Activities) != 8 {
		t.Fatalf("body=%s", r.Body.String())
	}
	for _, activity := range body.Activities {
		if activity["reward_available"] != false {
			t.Fatalf("invented reward: %#v", activity)
		}
	}
}
