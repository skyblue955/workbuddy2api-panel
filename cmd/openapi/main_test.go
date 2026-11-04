package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/workbuddyopen"
)

type fakeRedeemer struct {
	calls int
	token string
	input workbuddyopen.RedemptionRequest
}

func (f *fakeRedeemer) Redeem(_ context.Context, token string, input workbuddyopen.RedemptionRequest) (*workbuddyopen.RedemptionResponse, error) {
	f.calls++
	f.token, f.input = token, input
	return &workbuddyopen.RedemptionResponse{Status: "success", FlowNo: "flow", Credits: 7, OpenID: "fixture-open-id"}, nil
}

func TestRedeemDryRunDoesNotCallClient(t *testing.T) {
	f := &fakeRedeemer{}
	var out strings.Builder
	err := run([]string{"redeem"}, strings.NewReader(`{"code":"code","request_id":"stable-1"}`), &out, f)
	if err != nil || out.String() != "DRY_RUN: no network request\n" || f.calls != 0 {
		t.Fatalf("err=%v out=%q calls=%d", err, out.String(), f.calls)
	}
}

func TestRedeemExecuteReadsTokenAndDoesNotPrintSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")
	journal := filepath.Join(dir, "journal")
	tokenJSON := `{"access_token":"official-token","token_type":"Bearer","expires_in":3600,"scope":"user.credit.exchange","open_id":"fixture-open-id","obtained_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}`
	if err := os.WriteFile(path, []byte(tokenJSON), 0600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRedeemer{}
	var out strings.Builder
	err := run([]string{"redeem", "-execute", "-token-file", path, "-journal-dir", journal}, strings.NewReader(`{"gift_key":"giftkey","gift_code":"giftcode","request_id":"stable-2"}`), &out, f)
	if err != nil || f.calls != 1 || f.token != "official-token" || !strings.Contains(out.String(), `"flow_no":"flow"`) || !strings.Contains(out.String(), `"credits":7`) || strings.Contains(out.String(), "official-token") || strings.Contains(out.String(), "secret-open-id") {
		t.Fatalf("err=%v out=%q calls=%d token=%q", err, out.String(), f.calls, f.token)
	}
}

func TestRedeemRejectsUnknownTrailingAndOversizedInput(t *testing.T) {
	for _, input := range []string{`{"code":"code","request_id":"stable-1","extra":1}`, `{"code":"code","request_id":"stable-1"}{}`, strings.Repeat("x", maxInput+1)} {
		f := &fakeRedeemer{}
		if err := run([]string{"redeem"}, strings.NewReader(input), &strings.Builder{}, f); err == nil || f.calls != 0 {
			t.Fatalf("input accepted or called client: err=%v calls=%d", err, f.calls)
		}
	}
}

func TestRedeemTokenFilePermissionsAndExecuteRequirement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")
	journal := filepath.Join(dir, "journal")
	if err := os.WriteFile(path, []byte(`{"access_token":"secret"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"redeem", "-execute", "-token-file", path, "-journal-dir", journal}, strings.NewReader(`{"code":"code","request_id":"stable-1"}`), &strings.Builder{}, &fakeRedeemer{}); err == nil {
		t.Fatal("permissive token file accepted")
	}
	if err := run([]string{"redeem", "-execute"}, strings.NewReader(`{"code":"code","request_id":"stable-1"}`), &strings.Builder{}, &fakeRedeemer{}); err == nil {
		t.Fatal("missing token file accepted")
	}
}
