package api

import (
 "context"
 "encoding/json"
 "errors"
 "net/http"
 "net/http/httptest"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "time"

 "swarm/packages/swarmd/internal/permission"
 "swarm/packages/swarmd/internal/provider/claudecode"
 provideriface "swarm/packages/swarmd/internal/provider/interfaces"
 "swarm/packages/swarmd/internal/security"
 pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: standing MCP calls must use the exact V3 ToolInvoker's workspace and
// permission authority. A real bash tool writes only the session's workspace;
// an account deny must reject the next call with no marker side effect. This
// HTTP -> executor -> permission -> real tool test is the narrowest integration
// layer proving MCP does not introduce a permission or worktree bypass.
func TestSessionMCPCanonicalExecutorWorkspaceAndPermission(t *testing.T){
 f:=newReopenFixture(t)
 p:=testPrincipal()
 invoker,runCtx,job,_:=reopenedWorkerRun(t,f,"session-mcp-executor")
 ctx,cancel:=context.WithTimeout(runCtx,20*time.Second);defer cancel()
 credentials,err:=pebblestore.Open(filepath.Join(t.TempDir(),"credentials"));if err!=nil{t.Fatal(err)};defer credentials.Close()
 sec:=security.NewService(pebblestore.NewClientAuthStore(credentials),nil)
 endpoint:=claudecode.NewSessionMCP(sec,nil,t.TempDir())
 detach,err:=endpoint.WithToolExecutor(ctx,p,provideriface.Request{SessionID:job.SessionID,Tools:[]provideriface.ToolDefinition{{Name:"bash"}},ToolInvoker:invoker});if err!=nil{t.Fatal(err)};defer detach()
 f.server.SetSessionMCP(endpoint)
 token,err:=sec.CreateSessionToken(p.AccountScopeID,p.UserID,job.SessionID);if err!=nil{t.Fatal(err)}
 call:=func(id,args string)string{
  t.Helper()
  body,_:=json.Marshal(map[string]any{"jsonrpc":"2.0","id":1,"method":"tools/call","params":map[string]any{"name":"bash","arguments":json.RawMessage(args),"_meta":map[string]any{"claudecode/toolUseId":id}}})
  req:=httptest.NewRequest(http.MethodPost,"/mcp",strings.NewReader(string(body))).WithContext(ctx);req.Header.Set("Authorization","Bearer "+token)
  w:=httptest.NewRecorder();f.server.LoopbackAPIHandler().ServeHTTP(w,req)
  if w.Code!=http.StatusOK{t.Fatalf("MCP: %d %s",w.Code,w.Body)}
  return w.Body.String()
 }
 result:=call("session-mcp-bash",reopenBashArguments)
 if strings.Contains(result,`"isError":true`){t.Fatalf("approved tool failed: %s",result)}
 if raw,err:=os.ReadFile(filepath.Join(f.session.WorkspacePath,"built.txt"));err!=nil||string(raw)!="built"{t.Fatalf("tool workspace marker=%q %v",raw,err)}
 if _,err=workerReopenPermissions(t,f).UpsertRuleForAccount(p.AccountScopeID,permission.PolicyRule{Kind:permission.PolicyRuleKindTool,Tool:"bash",Decision:permission.PolicyDecisionDeny});err!=nil{t.Fatal(err)}
 result=call("session-mcp-denied",strings.ReplaceAll(reopenBashArguments,"built.txt","denied.txt"))
 if !strings.Contains(result,"denied by"){t.Fatalf("permission denial missing: %s",result)}
 if _,err=os.Stat(filepath.Join(f.session.WorkspacePath,"denied.txt"));!errors.Is(err,os.ErrNotExist){t.Fatalf("denied call wrote marker: %v",err)}
}
