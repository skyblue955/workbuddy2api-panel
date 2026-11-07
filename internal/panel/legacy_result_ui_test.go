package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestAccountTaskClaimUsesTypedReceiptTruth(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		status  int
		want    map[string]any
		wantErr string
	}{
		{"positive", `{"credit":100,"energy":5}`, http.StatusOK, map[string]any{"credit": float64(100), "energy": float64(5), "confirmed": true}, ""},
		{"already", `{"already_claimed":true}`, http.StatusOK, map[string]any{"already_claimed": true, "confirmed": true}, ""},
		{"zero_false", `{"credit":0,"energy":0,"already_claimed":false}`, http.StatusOK, map[string]any{"no_reward": true, "confirmed": false}, ""},
		{"zero_unknown", `{"credit":0,"energy":0}`, http.StatusOK, map[string]any{"no_reward": true, "confirmed": false}, ""},
		{"malformed", `null`, http.StatusBadGateway, nil, "claim: invalid reward response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":` + tc.data + `}`))
			}))
			defer srv.Close()
			p := &Panel{cfg: Config{Pool: pool.New(""), Upstream: &upstream.Client{HTTP: srv.Client(), WebBaseCN: srv.URL, ChatBaseCN: srv.URL}}}
			p.cfg.Pool.Add(&auth.Auth{UID: "fixture", AccessToken: "fixture"})
			req := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/fixture/tasks/claim", strings.NewReader(`{"task_code":"chat_5"}`))
			req.SetPathValue("uid", "fixture")
			rec := httptest.NewRecorder()
			p.accountTaskClaim(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			var got map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &got)
			if tc.wantErr != "" {
				if got["error"] != tc.wantErr || strings.Contains(rec.Body.String(), tc.data) {
					t.Fatalf("sanitized error=%v body=%s", got["error"], rec.Body.String())
				}
				return
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Fatalf("%s=%v want %v body=%s", k, got[k], want, rec.Body.String())
				}
			}
			if tc.name == "zero_false" && got["already_claimed"] == true {
				t.Fatal("zero false was treated as already claimed")
			}
		})
	}
}

func TestAutoResultSummaryDoesNotCountUnverifiedDone(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	_, file, _, _ := runtime.Caller(0)
	app := filepath.Join(filepath.Dir(file), "app.js")
	script := `const fs=require('fs'); const src=fs.readFileSync(process.argv[1],'utf8');
const a=src.indexOf('function summarizeAutoResults'); const b=src.indexOf('// 一键完成全部可自动任务',a);
const ctx={}; eval(src.slice(a,b)+';ctx.summarizeAutoResults=summarizeAutoResults');
const got=ctx.summarizeAutoResults([{status:'done',verified:true},{status:'done',verified:false},{status:'skipped'},{status:'unconfirmed'},{status:'unknown'},{status:'submitted'},{status:'done',verified:true,claim_error:'failed'},{status:'done',verified:true,no_reward:true},{status:'error'}]);
if (JSON.stringify(got)!==JSON.stringify({verifiedN:1,skippedN:1,unconfirmedN:6,errorN:1})) process.exit(1);`
	cmd := exec.Command(node, "-e", script, app)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("summary node test failed: %v: %s", err, out)
	}
}
