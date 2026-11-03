package panel

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

func TestEnterpriseSessionTaskRoutesRejectBeforeMutation(t *testing.T) {
	// token_source is deliberately a fixture-only JWT payload; no upstream call is made.
	payload, _ := json.Marshal(map[string]string{"token_source": "enterprise_switch"})
	token := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".fixture"
	a := &auth.Auth{UID: "enterprise-switch", AccessToken: token}
	accounts := pool.New("")
	accounts.Add(a)
	p := New(Config{APIKey: "fixture-key", Pool: accounts})
	for _, path := range []string{
		"/panel/api/accounts/enterprise-switch/tasks/auto",
		"/panel/api/accounts/enterprise-switch/tasks/auto_all",
	} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"task_code":"fixture"}`))
		req.Header.Set("Authorization", "Bearer fixture-key")
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if rec.Code != 422 {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "无需移除账号") || !strings.Contains(body, "不等于企业账号身份") {
			t.Fatalf("%s recovery guidance missing: %s", path, body)
		}
	}
}

func TestEnterpriseSessionTaskScanRejectsBeforeUpstream(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"token_source": "enterprise_switch"})
	token := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".fixture"
	accounts := pool.New("")
	accounts.Add(&auth.Auth{UID: "enterprise-scan", AccessToken: token})
	p := New(Config{APIKey: "fixture-key", Pool: accounts})
	req := httptest.NewRequest("POST", "/panel/api/tasks/scan_all", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer fixture-key")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "growth_error") || !strings.Contains(rec.Body.String(), "不代表企业账号") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAddAccountButtonDoesNotReuseReauthorizationUID(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `const fs=require('fs'),vm=require('vm');
const src=fs.readFileSync(process.argv[2],'utf8');
const start=src.indexOf('function openAdd('), end=src.indexOf("$('btnRefresh').onclick");
if(start<0||end<start) throw Error('add-account region missing');
const ids=['addVeil','addPick','addLoad','addReady','addDone','addErr','importDone','importErr','btnCopyUrl','btnOpenUrl','btnStartLogin','btnCloseAdd','btnAdd','addTabLogin','addTabImport','importFile','addUrl','addPoll','addReauthNote'];
const els={}; for(const id of ids) els[id]={hidden:false,disabled:false,textContent:'',dataset:{},classList:{on:false,add(){this.on=true},remove(){this.on=false}}};
const realms=[{value:'cn',checked:true,disabled:false},{value:'global',checked:false,disabled:false}];
const tabs=[{dataset:{tab:'login'},classList:{on:false,toggle(v){this.on=v}}},{dataset:{tab:'import'},classList:{on:false,toggle(v){this.on=v}}}];
const doc={querySelectorAll:(q)=>q==='input[name="addRealm"]'?realms:q==='#addTabs .tab'?tabs:[],querySelector:(q)=>q==='input[name="addRealm"]:checked'?realms.find(x=>x.checked):null};
const ctx={console,Array,Object,JSON,encodeURIComponent,setInterval,clearInterval,document:doc,$:id=>els[id],api:()=>Promise.reject(Error('unexpected network')),toast:()=>{}};
 vm.createContext(ctx); vm.runInContext('let loginTimer=null,loginState=null,reauthUID=null;'+src.slice(start,end),ctx); vm.runInContext('this.getReauth=()=>reauthUID',ctx);
ctx.openAdd('enterprise-switch','global');
if(ctx.getReauth()!=='enterprise-switch') throw Error('reauthorization UID not set');
if(!realms[0].disabled||!realms[1].disabled||!realms[1].checked||realms[0].checked) throw Error('reauthorization realm was not locked');
if(els.addReauthNote.hidden||!els.addReauthNote.textContent) throw Error('reauthorization notice missing');
ctx.switchAddTab('import'); if(els.addTabLogin.hidden||!els.addTabImport.hidden) throw Error('import tab was not blocked');
ctx.openAdd('enterprise-switch'); els.btnAdd.onclick();
if(ctx.getReauth()!==null) throw Error('normal add retained reauthorization UID');
if(realms[0].disabled||realms[1].disabled||!realms[0].checked||realms[1].checked||!els.addReauthNote.hidden) throw Error('normal add mode retained reauthorization state');
if(!els.addVeil.classList.on || els.addPick.hidden || !els.addLoad.hidden || !els.addErr.hidden) throw Error('normal add mode was not reset');
process.stdout.write('ok');`
	tmp := t.TempDir() + "/ui.cjs"
	if err := os.WriteFile(tmp, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, tmp, "app.js")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("UI harness failed: %v output=%s", err, out)
	}
}

func TestAccountRowAndReauthorizationCopyUseIdentityNeutralLanguage(t *testing.T) {
	src, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	// The rendered row must not turn a source hint into an enterprise identity.
	for _, required := range []string{
		"s.enterprise ? ' <span class=\"realm-tag\">企业版</span>' : (s.enterprise_switch_source ? ' <span class=\"realm-tag\">登录来源待核实</span>' : '')",
		"更新登录授权",
		"当前登录来源提示尚未核实，旧版任务自动化暂未验证；仅展示官方任务记录。这不等于企业账号",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("app.js missing identity-neutral contract: %q", required)
		}
	}
	for _, forbidden := range []string{"浏览器退出企业会话后，以个人身份重新授权", "退出企业会话后，再次使用个人身份登录"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("app.js retained stale reauthorization instruction: %q", forbidden)
		}
	}
}
