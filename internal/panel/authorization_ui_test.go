package panel

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestAuthorizationStatusHelperStatesAndEscaping(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `const fs=require('fs'),vm=require('vm');
const src=fs.readFileSync(process.argv[2],'utf8');
const start=src.indexOf('/* AUTH_STATUS_START'),end=src.indexOf('/* AUTH_STATUS_END */');
if(start<0||end<start) throw Error('authorization helper block missing');
const button={className:'',textContent:'',disabled:false,attrs:{},setAttribute(k,v){this.attrs[k]=v}};
const ctx={Map,Object,Date,JSON,esc:s=>String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),$:id=>button};
vm.createContext(ctx); vm.runInContext(src.slice(start,end),ctx); vm.runInContext('this.setFeedback=(u,v)=>authorizationFeedback.set(u,v)',ctx);
const now=1700000000000; ctx.Date={now:()=>now};
function view(account,feedback){ return ctx.authorizationView(account,feedback); }
const base={uid:'<x>',realm:'cn',authorization:{state:'valid',expires_at:now/1000+60}};
const checks=[
  [view(base,'pending'), 'pending', true],
  [view(base,'failed'), 'warning', false],
  [view(base,'timeout'), 'expired', false],
  [view(base), 'valid', false],
  [view({...base,authorization:{state:'valid',expires_at:now/1000}}), 'expired', false],
  [view({...base,authorization:{state:'missing'}}), 'idle', false],
  [view({...base,authorization:{}}), 'warning', false],
  [view({...base,enterprise_switch_source:true,manual_disabled:true,rate_limited_models:[{model:'x'}]}), 'valid', false],
];
for(const [got,state,disabled] of checks) if(got.state!==state||!!got.disabled!==disabled) throw Error(JSON.stringify({got,state,disabled}));
ctx.setFeedback('<x>','pending');
const html=ctx.authorizationButton(base);
if(!html.includes('data-u="&lt;x&gt;"')||!html.includes('auth-pending')||!html.includes(' disabled ')) throw Error('button escaping/state missing: '+html);
process.stdout.write('ok');`
	tmp := t.TempDir() + "/authorization.cjs"
	if err := os.WriteFile(tmp, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, tmp, "app.js")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("authorization helper harness failed: %v output=%s", err, out)
	}
}

func TestAuthorizationModalLifecycleEpochAndTimeout(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `(async()=>{const fs=require('fs'),vm=require('vm');
const src=fs.readFileSync(process.argv[2],'utf8');
const hs=src.indexOf('/* AUTH_STATUS_START'),he=src.indexOf('/* AUTH_STATUS_END */')+'/* AUTH_STATUS_END */'.length;
const start=src.indexOf('function openAdd('),end=src.indexOf("$('importFile').onchange");
if(hs<0||start<0||end<start) throw Error('lifecycle source region missing');
const ids=['addVeil','addPick','addLoad','addReady','addDone','addErr','importDone','importErr','btnCopyUrl','btnOpenUrl','btnStartLogin','btnCloseAdd','btnAdd','addTabLogin','addTabImport','importFile','addUrl','addPoll','addReauthNote'];
const els={}; for(const id of ids) els[id]={hidden:false,disabled:false,textContent:'',className:'',attrs:{},dataset:{},classList:{on:false,add(){this.on=true},remove(){this.on=false}},setAttribute(k,v){this.attrs[k]=v}};
const realms=[{value:'cn',checked:true,disabled:false},{value:'global',checked:false,disabled:false}];
const tabs=[{dataset:{tab:'login'},classList:{toggle(){}}},{dataset:{tab:'import'},classList:{toggle(){}}}];
const doc={querySelectorAll:q=>q==='input[name="addRealm"]'?realms:q==='#addTabs .tab'?tabs:[],querySelector:q=>q==='input[name="addRealm"]:checked'?realms.find(x=>x.checked):null};
let deferred=[]; const account={uid:'u',realm:'cn',authorization:{state:'valid',expires_at:9999999999}};
const ctx={console,Array,Object,JSON,Date,encodeURIComponent,setInterval,clearInterval,document:doc,$:id=>els[id],overviewData:{accounts:[account]},loadOverview:()=>{},esc:s=>String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),api:path=>new Promise((resolve,reject)=>deferred.push({path,resolve,reject})),toast:()=>{},open:()=>{},navigator:{clipboard:{writeText:()=>Promise.resolve()}},setTimeout,clearTimeout};
vm.createContext(ctx); vm.runInContext('function renderAccounts(){};let loginTimer=null,loginState=null,reauthUID=null;'+src.slice(hs,he)+src.slice(start,end),ctx);
ctx.openAdd('u','cn'); const first=ctx.startAddLogin();
if(!els.btnStartLogin.disabled||!els.btnStartLogin.className.includes('auth-pending')) throw Error('pending visual missing');
deferred.shift().resolve({state:'s1',url:'fixture'}); await first; const poll=ctx.pollLogin();
deferred.shift().resolve({done:true,reauthorized:true,nickname:'fixture',authorization:{state:'valid',expires_at:9999999999}}); await poll;
if(!els.btnStartLogin.className.includes('auth-valid')||!els.btnStartLogin.disabled||account.authorization.state!=='valid'||!els.addDone.textContent.includes('登录授权已更新')) throw Error('success lifecycle missing');
ctx.openAdd('u','cn'); deferred=[]; const failed=ctx.startAddLogin(); deferred.shift().reject(Error('fixture failure')); await failed;
if(!els.btnStartLogin.className.includes('auth-warning')||account.authorization.state!=='valid') throw Error('failure did not preserve old credential');
ctx.openAdd('u','cn'); const timedStart=ctx.startAddLogin(); deferred.shift().resolve({state:'s2',url:'fixture'}); await timedStart; ctx.Date={now:()=>Date.now()+16*60*1000}; await ctx.pollLogin();
if(!els.btnStartLogin.className.includes('auth-expired')) throw Error('timeout visual missing');
// A poll that never resolves must still expire via the deadline check; a late
// success must not turn the expired state green or patch the cached account.
ctx.openAdd('u','cn'); let clock=0; ctx.Date={now:()=>clock}; const stalledPollStart=ctx.startAddLogin();
deferred.shift().resolve({state:'s3',url:'fixture'}); await stalledPollStart; const latePoll=ctx.pollLogin();
clock=15*60*1000+1; await ctx.pollLogin();
if(!els.btnStartLogin.className.includes('auth-expired')||account.authorization.state!=='valid') throw Error('unresolved poll did not expire safely');
deferred.shift().resolve({done:true,reauthorized:true,authorization:{state:'expired'}}); await latePoll;
if(!els.btnStartLogin.className.includes('auth-expired')||account.authorization.state!=='valid') throw Error('late poll revived expired state');
// A stalled login/start request is covered by the same watchdog and its late
// response must be ignored after timeout.
ctx.openAdd('u','cn'); clock=0; const stalledStart=ctx.startAddLogin(); clock=15*60*1000+1; await ctx.pollLogin();
if(!els.btnStartLogin.className.includes('auth-expired')) throw Error('stalled start did not expire');
deferred.shift().resolve({state:'late',url:'fixture'}); await stalledStart;
if(!els.btnStartLogin.className.includes('auth-expired')) throw Error('late start revived expired state');
ctx.openAdd('u','cn'); const pending=ctx.startAddLogin(); if(!els.btnStartLogin.disabled) throw Error('pending not disabled'); els.btnCloseAdd.onclick(); deferred.shift().resolve({state:'stale',url:'fixture'}); await pending; ctx.openAdd('u','cn');
if(!els.btnStartLogin.className.includes('auth-idle')||els.btnStartLogin.disabled) throw Error('cancel left pending state');
process.stdout.write('ok');})().catch(e=>{console.error(e);process.exit(1)});`
	tmp := t.TempDir() + "/lifecycle.cjs"
	if err := os.WriteFile(tmp, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, tmp, "app.js")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("authorization lifecycle harness failed: %v output=%s", err, out)
	}
}
