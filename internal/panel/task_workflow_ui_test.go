package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTaskWorkflowUIBoundariesAndResultTruth(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Join(filepath.Dir(file), "app.js")
	script := `(async()=>{const fs=require('fs'),vm=require('vm'),assert=require('assert');const src=fs.readFileSync(process.argv[2],'utf8');
const a=src.indexOf('/* TASK_WORKFLOW_UI_START'),b=src.indexOf('/* TASK_WORKFLOW_UI_END */')+'/* TASK_WORKFLOW_UI_END */'.length;
function harness({prompts=['deepseek-v4-flash','1'],confirms=[true,true],hook}={}) {
 const calls=[],notices=[],btn={disabled:false,textContent:'run'},state={hidden:true,textContent:''};let confirmCount=0,loads=0;
 const body={addEventListener:()=>{},querySelectorAll:()=>[]};
 const ctx={crypto:{randomUUID:()=> '12345678-1234-4234-8234-123456789abc'},Date,Math,Number,String,JSON,encodeURIComponent,
  $:id=>id==='taskBody'?body:id==='taskState'?state:btn,prompt:()=>prompts.shift(),confirm:()=>{confirmCount++;return confirms.shift()===true},
  toast:(...v)=>notices.push(v),loadTasks:async()=>{loads++},api:async(path,opts)=>{const req={path,body:JSON.parse(opts.body)};calls.push(req);
   if(hook)return hook(req,ctx,calls);
   return req.body.execute?{status:'chat_failed',chat_calls:1}:{ok:true,tasks:[{task_code:'chat_5',can_execute:true}]};}};
 vm.createContext(ctx);vm.runInContext("let taskUID='fixture',taskWorkflowPending=false;"+src.slice(a,b),ctx);
 return {ctx,calls,btn,state,notices,get confirms(){return confirmCount},get loads(){return loads},run:()=>ctx.workflowExecute({task_code:'chat_5'},btn)};
}
for(const invalid of ['6','0','1.5','abc']){const h=harness({prompts:['deepseek-v4-flash',invalid]});await h.run();assert.equal(h.calls.length,1);assert(!h.calls[0].body.execute);assert.equal(h.confirms,0)}
{const h=harness({confirms:[false]});await h.run();assert.equal(h.calls.length,1);assert.equal(h.confirms,1);assert(!h.btn.disabled)}
{const h=harness({confirms:[true,false]});await h.run();assert.equal(h.calls.length,2);const req=h.calls[1].body;assert(req.execute);assert.equal(req.max_output_tokens,16);assert.equal(req.max_chat_calls,1);assert.equal(req.claim_rewards,false);assert(/^[A-Za-z0-9_-]{8,64}$/.test(req.operation_id));assert.equal(h.confirms,2);assert.equal(h.loads,1)}
{const h=harness({confirms:[true,true]});await h.run();assert.equal(h.calls[1].body.claim_rewards,true)}
{const h=harness();await h.ctx.workflowCheck();assert.deepEqual(h.calls[0].body,{});assert.equal(h.confirms,0)}
{let release;const wait=new Promise(r=>release=r);const h=harness({hook:async(req)=>{await wait;return {ok:true,tasks:[{task_code:'chat_5',can_execute:true}]}}});const first=h.run();await h.run();assert.equal(h.calls.length,1);vm.runInContext("taskUID='different'",h.ctx);release();await first;assert.equal(h.calls.length,1);assert.equal(h.confirms,0);assert.equal(h.loads,0)}
{const h=harness({hook:async(req)=>{if(req.body.execute)throw Error('network');return {ok:true,tasks:[{task_code:'chat_5',can_execute:true}]}}});await h.run();assert.equal(h.calls.length,2);const original=JSON.stringify(h.calls[1]);await h.run();assert.equal(h.calls.length,3);assert.equal(JSON.stringify(h.calls[2]),original);assert.equal(h.confirms,2);assert.equal(h.btn.textContent,'核对上次执行')}
{const h=harness();const f=h.ctx.workflowResultText;
 assert.equal(f({status:'planned'}),'仅完成条件检查，未执行');assert(f({status:'chat_failed'}).includes('对话未确认成功'));
 for(const missing of ['reward_received','receipt_reward_reported','upstream_claimed','balance_verified']){const r={status:'claim_outcome_uncertain',reward_received:true,receipt_reward_reported:true,upstream_claimed:true,balance_verified:true};r[missing]=false;assert(!f(r).includes('已确认到账'))}
 assert(f({status:'reward_confirmed',reward_received:true,receipt_reward_reported:true,upstream_claimed:true,balance_verified:true}).includes('已确认到账'));
}
process.stdout.write('ok');})().catch(e=>{console.error(e);process.exit(1)});`

	tmp := filepath.Join(t.TempDir(), "workflow.cjs")
	if err := os.WriteFile(tmp, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, tmp, src)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("workflow UI harness failed: %v output=%s", err, out)
	}
}
