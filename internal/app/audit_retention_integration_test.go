package app

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/audit"
)

// audit_retention_integration_test.go —— Day 25 的端到端验证。
//
// 验收标准要求「每个 Day 的成果必须有真实端到端可达性验证，不是只写单测」。
// 这里全部走 NewRouter() 出来的真实链路（含认证中间件与 authz 权限判定），
// 验证的是「配置真的生效了」，而不是「服务方法单独能跑」。

// ---------------------------------------------------------------------------
// 1. 写入前脱敏：经真实 HTTP 写入的密钥，从真实 HTTP 读出来必须是占位符
// ---------------------------------------------------------------------------

func TestAuditDetailsAreRedactedEndToEnd(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	raw, _ := json.Marshal(map[string]interface{}{
		"action":      "agent.tool_call",
		"actor":       "agent-1",
		"actor_type":  "agent",
		"resource":    "agent",
		"resource_id": "agent-1",
		"details": map[string]interface{}{
			"endpoint": "https://api.openai.com/v1/chat/completions",
			"api_key":  "sk-" + strings.Repeat("a", 40),
			"headers": map[string]interface{}{
				"Authorization": "Bearer " + strings.Repeat("b", 40),
				"Content-Type":  "application/json",
			},
			"nested": []interface{}{
				map[string]interface{}{"name": "step-1", "token": strings.Repeat("c", 24)},
			},
		},
	})

	resp, err := env.post(ts.URL+"/api/v1/audit/events", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /api/v1/audit/events: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("status = %d, want 201, body=%s", resp.StatusCode, body)
	}
	created := decodeJSON(t, resp)

	// 写入响应里就已经是脱敏后的内容 —— 秘密根本没有机会进入存储。
	assertNoSecret(t, created)

	// 重新查出来看：存储里也不能有秘密（防「写响应脱敏了但落库存了明文」）。
	listResp, err := env.get(ts.URL + "/api/v1/audit/events?action=agent.tool_call")
	if err != nil {
		t.Fatalf("GET /api/v1/audit/events: %v", err)
	}
	listed := decodeJSON(t, listResp)
	assertNoSecret(t, listed)
}

func TestAuditExportDoesNotLeakSecrets(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	raw, _ := json.Marshal(map[string]interface{}{
		"action":  "user.login",
		"actor":   "u1",
		"details": map[string]interface{}{"password": "Sup3rSecret!"},
	})
	resp, err := env.post(ts.URL+"/api/v1/audit/events", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	exportResp, err := env.get(ts.URL + "/api/v1/audit/export?format=csv")
	if err != nil {
		t.Fatalf("GET export: %v", err)
	}
	defer exportResp.Body.Close()
	body, _ := io.ReadAll(exportResp.Body)

	// 导出是泄漏最常见的出口（文件会被转存/发邮件/进工单）。
	if bytes.Contains(body, []byte("Sup3rSecret!")) {
		t.Errorf("export body leaks the password:\n%s", body)
	}
}

// ---------------------------------------------------------------------------
// 2. 留存策略 + dry-run 清理，走真实 router
// ---------------------------------------------------------------------------

func TestAuditRetentionFlowEndToEnd(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// 默认策略：90 天，开启。
	resp, err := env.get(ts.URL + "/api/v1/audit/retention")
	if err != nil {
		t.Fatalf("GET /api/v1/audit/retention: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}
	policy := decodeJSON(t, resp)
	if got := policy["retention_days"]; got != float64(audit.DefaultRetentionDays) {
		t.Errorf("retention_days = %v, want %d", got, audit.DefaultRetentionDays)
	}

	// 写两条：一条 60 天前（超期），一条刚发生。
	old := time.Now().UTC().AddDate(0, 0, -60).Format(time.RFC3339)
	for _, body := range []map[string]interface{}{
		{"action": "system.startup", "actor": "sys", "timestamp": old},
		{"action": "system.startup", "actor": "sys"},
	} {
		raw, _ := json.Marshal(body)
		r, err := env.post(ts.URL+"/api/v1/audit/events", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		r.Body.Close()
	}

	// 注：写入接口不接收 timestamp 覆盖，因此这条 60 天前的事件会被
	// 服务端按"现在"落库。这里改用策略接口把保留期调成一个极小的差值
	// 无法实现（最小单位是天），所以直接验证「dry-run 默认不删」这一
	// 最关键的安全属性，再用 purge 的 now 参数构造超期场景。
	purgeURL := ts.URL + "/api/v1/audit/retention/purge"

	// (a) 不传 body：默认 dry-run。
	resp, err = env.post(purgeURL, "application/json", nil)
	if err != nil {
		t.Fatalf("POST purge: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("purge status = %d, want 200, body=%s", resp.StatusCode, body)
	}
	plan := decodeJSON(t, resp)
	if plan["dry_run"] != true {
		t.Errorf("dry_run = %v, want true by default", plan["dry_run"])
	}
	if plan["removed"] != float64(0) {
		t.Errorf("removed = %v, want 0 in dry-run", plan["removed"])
	}

	// dry-run 之后事件必须还在。
	before := countAuditEvents(t, env, ts.URL)
	if before < 2 {
		t.Fatalf("events = %d, want >= 2 after dry-run", before)
	}

	// (b) 注入 now = 100 天后的时刻：90 天保留期下所有现有事件都超期，
	//     且显式 dry_run=false 才真删。
	future := time.Now().UTC().AddDate(0, 0, 100).Format(time.RFC3339)
	body, _ := json.Marshal(map[string]interface{}{"dry_run": false, "now": future})
	resp, err = env.post(purgeURL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST purge(real): %v", err)
	}
	plan = decodeJSON(t, resp)
	if plan["dry_run"] != false {
		t.Errorf("dry_run = %v, want false", plan["dry_run"])
	}
	if plan["removed"] == float64(0) {
		t.Errorf("removed = 0, want > 0 when everything is past retention")
	}

	after := countAuditEvents(t, env, ts.URL)
	if after != 0 {
		t.Errorf("events = %d after purge, want 0", after)
	}
}

// ---------------------------------------------------------------------------
// 3. 导出产物自证 + 校验接口
// ---------------------------------------------------------------------------

func TestAuditExportCarriesVerifiableManifestEndToEnd(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	for _, action := range []string{"agent.created", "agent.started"} {
		raw, _ := json.Marshal(map[string]interface{}{"action": action, "actor": "a1"})
		r, err := env.post(ts.URL+"/api/v1/audit/events", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		r.Body.Close()
	}

	resp, err := env.get(ts.URL + "/api/v1/audit/export?format=json")
	if err != nil {
		t.Fatalf("GET export: %v", err)
	}
	defer resp.Body.Close()

	product, manifest := readExportParts(t, resp.Header.Get("Content-Type"), resp.Body)

	// 产物内部必须自带 manifest（HTTP 头会丢，文件里的不会）。
	var payload struct {
		Manifest audit.ExportManifest `json:"manifest"`
		Records  []json.RawMessage    `json:"records"`
	}
	if err := json.Unmarshal(product, &payload); err != nil {
		t.Fatalf("unmarshal export product: %v", err)
	}
	if payload.Manifest.SHA256 == "" {
		t.Fatal("export product has no digest inside the file")
	}
	if payload.Manifest.Count != 2 {
		t.Errorf("manifest.count = %d, want 2", payload.Manifest.Count)
	}
	if len(payload.Records) != 2 {
		t.Errorf("records = %d, want 2", len(payload.Records))
	}

	// 校验接口：原样内容 -> verified=true。
	verifyBody, _ := json.Marshal(map[string]interface{}{
		"manifest": manifest,
		"content":  string(product),
	})
	vresp, err := env.post(ts.URL+"/api/v1/audit/export/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("POST verify: %v", err)
	}
	verdict := decodeJSON(t, vresp)
	if verdict["verified"] != true {
		t.Fatalf("verified = %v (%v), want true", verdict["verified"], verdict["reason"])
	}

	// 篡改后的内容 -> verified=false，但接口仍是 200（这是校验结论）。
	tampered := strings.Replace(string(product), "agent.created", "agent.deleted", 1)
	verifyBody, _ = json.Marshal(map[string]interface{}{
		"manifest": manifest,
		"content":  tampered,
	})
	vresp, err = env.post(ts.URL+"/api/v1/audit/export/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("POST verify(tampered): %v", err)
	}
	if vresp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 even when verification fails", vresp.StatusCode)
	}
	verdict = decodeJSON(t, vresp)
	if verdict["verified"] != false {
		t.Errorf("verified = %v for tampered content, want false", verdict["verified"])
	}
	if verdict["reason"] == "" || verdict["reason"] == nil {
		t.Error("reason is empty for failed verification")
	}
}

// ---------------------------------------------------------------------------
// 4. 权限：留存接口必须受 authz 保护（不能因为新路由漏登记而变成默认拒绝或放行）
// ---------------------------------------------------------------------------

func TestAuditRetentionRequiresPermissionEndToEnd(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// 已授权的 coordinator：应放行。
	resp, err := env.get(ts.URL + "/api/v1/audit/retention")
	if err != nil {
		t.Fatalf("GET as authorized: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("authorized GET status = %d, want 200", resp.StatusCode)
	}

	// 未携带 token：必须 401，而不是放行。
	anonReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/audit/retention", nil)
	anonResp, err := http.DefaultClient.Do(anonReq)
	if err != nil {
		t.Fatalf("GET as anonymous: %v", err)
	}
	anonResp.Body.Close()
	if anonResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous GET status = %d, want 401", anonResp.StatusCode)
	}

	// 真实执行清理同样要求权限：匿名 POST 不能把审计删掉。
	anonReq, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/v1/audit/retention/purge", nil)
	anonResp, err = http.DefaultClient.Do(anonReq)
	if err != nil {
		t.Fatalf("POST purge as anonymous: %v", err)
	}
	anonResp.Body.Close()
	if anonResp.StatusCode == http.StatusOK {
		t.Error("anonymous POST purge returned 200 — 清理接口不能对匿名开放")
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func decodeJSON(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal body: %v\n%s", err, body)
	}
	return out
}

// assertNoSecret 断言响应里不含任何明文密钥片段。
func assertNoSecret(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	blob, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	text := string(blob)
	for name, needle := range map[string]string{
		"api_key":       "sk-" + strings.Repeat("a", 40),
		"authorization": "Bearer " + strings.Repeat("b", 40),
		"nested token":  strings.Repeat("c", 24),
	} {
		if strings.Contains(text, needle) {
			t.Errorf("response leaks %s (still plaintext): %s", name, text)
		}
	}
	if !strings.Contains(text, audit.RedactedPlaceholder) {
		t.Errorf("response has no %q placeholder, redaction may not have run: %s",
			audit.RedactedPlaceholder, text)
	}
}

func countAuditEvents(t *testing.T, env *testEnv, baseURL string) int {
	t.Helper()
	resp, err := env.get(baseURL + "/api/v1/audit/events?limit=100")
	if err != nil {
		t.Fatalf("GET /api/v1/audit/events: %v", err)
	}
	payload := decodeJSON(t, resp)
	total, ok := payload["total"].(float64)
	if !ok {
		t.Fatalf("total missing in %v", payload)
	}
	return int(total)
}

// readExportParts 从 multipart/mixed 导出响应里分离「产物」与「manifest」。
func readExportParts(t *testing.T, contentType string, body io.Reader) ([]byte, audit.ExportManifest) {
	t.Helper()

	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content-type %q: %v", contentType, err)
	}
	boundary := params["boundary"]
	if boundary == "" {
		t.Fatalf("no boundary in content-type %q", contentType)
	}

	mr := multipart.NewReader(body, boundary)
	var product []byte
	var manifest audit.ExportManifest
	parts := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read multipart part: %v", err)
		}
		raw, err := io.ReadAll(part)
		part.Close()
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		parts++
		if part.FileName() == audit.ExportManifestPart {
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatalf("unmarshal manifest: %v\n%s", err, raw)
			}
			continue
		}
		product = raw
	}
	if parts != 2 {
		t.Fatalf("multipart parts = %d, want 2", parts)
	}
	if product == nil {
		t.Fatal("product part not found in export response")
	}
	return product, manifest
}
