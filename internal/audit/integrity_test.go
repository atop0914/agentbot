package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// integrity_test.go 覆盖 Day 25 的三件核心能力：
// 写入前脱敏、留存策略与 dry-run 清理、导出完整性校验。

// --- 脱敏：写入前替换，而不是读取侧过滤 ---

func TestRedact_CredentialsAreReplacedBeforeStorage(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	_, err := svc.LogEvent(ctx, Event{
		Action: "agent.tool_call",
		Actor:  "agent-1",
		Details: map[string]interface{}{
			"endpoint": "https://api.example.com/v1/chat",
			"api_key":  "sk-live-0123456789abcdef",
			"headers": map[string]interface{}{
				"Authorization": "Bearer " + strings.Repeat("t", 32),
				"X-Trace-Id":    "trace-abc",
			},
		},
	})
	if err != nil {
		t.Fatalf("LogEvent: %v", err)
	}

	// 关键断言：**从仓库读出来的原文里不能出现秘密**。
	// 如果脱敏放在读取侧，这里读到的就是明文。
	records, err := svc.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	details := records[0].Details

	if details["api_key"] != RedactedPlaceholder {
		t.Errorf("api_key = %v, want %q", details["api_key"], RedactedPlaceholder)
	}
	// 字段名必须保留，否则调用方按字段名解析会断。
	if _, ok := details["api_key"]; !ok {
		t.Error("api_key key was removed, want key preserved")
	}
	// 非敏感字段不受影响。
	if details["endpoint"] != "https://api.example.com/v1/chat" {
		t.Errorf("endpoint = %v, want untouched", details["endpoint"])
	}

	nested, ok := details["headers"].(map[string]interface{})
	if !ok {
		t.Fatalf("headers = %#v, want map", details["headers"])
	}
	if nested["Authorization"] != RedactedPlaceholder {
		t.Errorf("Authorization = %v, want %q", nested["Authorization"], RedactedPlaceholder)
	}
	if nested["X-Trace-Id"] != "trace-abc" {
		t.Errorf("X-Trace-Id = %v, want untouched", nested["X-Trace-Id"])
	}

	// 被脱敏的键路径要登记，便于排查时知道「这里原本有凭证」。
	paths, ok := details[RedactedKeysField].([]string)
	if !ok {
		t.Fatalf("%s = %#v, want list of paths", RedactedKeysField, details[RedactedKeysField])
	}
	joined := strings.Join(paths, ",")
	for _, want := range []string{"api_key", "headers.Authorization"} {
		if !strings.Contains(joined, want) {
			t.Errorf("redacted paths = %v, want to contain %q", paths, want)
		}
	}

	// 原始事件对象不被就地修改（调用方可能继续使用它）。
	original := Event{Details: map[string]interface{}{"token": "abc"}}
	redacted, paths2 := RedactDetails(original.Details)
	if original.Details["token"] != "abc" {
		t.Error("RedactDetails mutated the input map")
	}
	if redacted["token"] != RedactedPlaceholder || len(paths2) != 1 {
		t.Errorf("redacted = %v paths = %v", redacted, paths2)
	}
}

func TestIsSensitiveKey(t *testing.T) {
	sensitive := []string{
		"password", "Password", "PASSWORD",
		"api_key", "API_KEY", "x-api-key",
		"authorization", "Authorization",
		"token", "access_token", "refresh_token",
		"client_secret", "private_key", "secret_access_key",
		"cookie", "set-cookie", "session_id",
		"db_password", "github_token", "openai_api_key",
	}
	for _, k := range sensitive {
		if !IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = false, want true", k)
		}
	}

	insensitive := []string{
		"", "endpoint", "user_id", "trace_id", "status",
		"action", "resource", "key", "monkey", "secretary",
	}
	for _, k := range insensitive {
		if IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = true, want false", k)
		}
	}
}

func TestRedactDetails_NestedSlicesAndEmptyValues(t *testing.T) {
	in := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"name": "a", "token": "t1"},
			map[string]interface{}{"name": "b", "token": ""}, // 空值不脱敏
		},
		"password": nil, // 空值不脱敏
	}
	out, paths := RedactDetails(in)

	if out["password"] != nil {
		t.Errorf("password = %v, want nil preserved", out["password"])
	}
	items := out["items"].([]interface{})
	first := items[0].(map[string]interface{})
	if first["token"] != RedactedPlaceholder {
		t.Errorf("items[0].token = %v, want placeholder", first["token"])
	}
	second := items[1].(map[string]interface{})
	if second["token"] != "" {
		t.Errorf("items[1].token = %v, want empty preserved", second["token"])
	}
	if len(paths) != 1 || paths[0] != "items[0].token" {
		t.Errorf("paths = %v, want [items[0].token]", paths)
	}
}

// --- 留存策略与 dry-run ---

func TestRetention_DefaultPolicyIsFinite(t *testing.T) {
	svc := newTestService(t)
	rs := NewRetentionService(svc)
	if rs == nil {
		t.Fatal("NewRetentionService returned nil")
	}
	p := rs.Policy()
	if !p.Enabled {
		t.Error("default policy disabled, want enabled")
	}
	if p.RetentionDays != DefaultRetentionDays {
		t.Errorf("retention_days = %d, want %d", p.RetentionDays, DefaultRetentionDays)
	}
	// 「什么都没配」绝不能变成无限期保留。
	if p.Unlimited() {
		t.Error("default policy is unlimited, want finite retention")
	}
}

func TestRetention_DryRunCountsWithoutDeleting(t *testing.T) {
	svc := newTestService(t)
	rs := NewRetentionService(svc)
	ctx := context.Background()

	if _, err := rs.SetPolicy(RetentionPolicy{Enabled: true, RetentionDays: 30}, "admin"); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -60)
	fresh := now.AddDate(0, 0, -1)

	mustLog(t, svc, Event{Action: "agent.created", Actor: "a1", Timestamp: old})
	mustLog(t, svc, Event{Action: "agent.created", Actor: "a2", Timestamp: old})
	mustLog(t, svc, Event{Action: "agent.created", Actor: "a3", Timestamp: fresh})

	plan, err := rs.Purge(ctx, now, true)
	if err != nil {
		t.Fatalf("Purge(dry-run): %v", err)
	}
	if !plan.DryRun {
		t.Error("plan.DryRun = false, want true")
	}
	if plan.Removed != 0 {
		t.Errorf("plan.Removed = %d, want 0 in dry-run", plan.Removed)
	}
	if plan.Candidates != 2 {
		t.Errorf("plan.Candidates = %d, want 2", plan.Candidates)
	}

	// dry-run 之后数据必须原样还在。
	total, err := svc.Count(ctx, Filter{})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if total != 3 {
		t.Fatalf("count after dry-run = %d, want 3 (dry-run must not delete)", total)
	}

	// 真实执行：删除条数应与预演候选一致。
	plan2, err := rs.Purge(ctx, now, false)
	if err != nil {
		t.Fatalf("Purge(real): %v", err)
	}
	if plan2.DryRun {
		t.Error("plan2.DryRun = true, want false")
	}
	if plan2.Removed != 2 {
		t.Errorf("plan2.Removed = %d, want 2", plan2.Removed)
	}

	total, _ = svc.Count(ctx, Filter{})
	if total != 1 {
		t.Errorf("count after purge = %d, want 1", total)
	}
}

func TestRetention_UnlimitedNeverPurges(t *testing.T) {
	svc := newTestService(t)
	rs := NewRetentionService(svc)
	ctx := context.Background()

	if _, err := rs.SetPolicy(RetentionPolicy{Enabled: true, RetentionDays: UnlimitedRetention}, "admin"); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	mustLog(t, svc, Event{Action: "agent.created", Timestamp: time.Now().UTC().AddDate(-5, 0, 0)})

	plan, err := rs.Purge(ctx, time.Now().UTC(), false)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if plan.Removed != 0 || plan.Candidates != 0 {
		t.Errorf("plan = %+v, want nothing removed for unlimited retention", plan)
	}
	total, _ := svc.Count(ctx, Filter{})
	if total != 1 {
		t.Errorf("count = %d, want 1 (unlimited retention must not delete)", total)
	}
}

func TestRetention_NegativeDaysRejected(t *testing.T) {
	svc := newTestService(t)
	rs := NewRetentionService(svc)

	// 负数没有语义，静默回落会让「配置错了但看起来生效了」蒙混过关。
	if _, err := rs.SetPolicy(RetentionPolicy{Enabled: true, RetentionDays: -7}, "admin"); err == nil {
		t.Fatal("SetPolicy(-7) = nil error, want ErrInvalidRetention")
	}
	if got := rs.Policy().RetentionDays; got != DefaultRetentionDays {
		t.Errorf("retention_days = %d, want unchanged %d", got, DefaultRetentionDays)
	}
}

func TestRetention_PolicyRecordsActor(t *testing.T) {
	svc := newTestService(t)
	rs := NewRetentionService(svc)

	p, err := rs.SetPolicy(RetentionPolicy{Enabled: true, RetentionDays: 180}, "user-42")
	if err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	if p.UpdatedBy != "user-42" {
		t.Errorf("updated_by = %q, want user-42 (变更必须可追溯到人)", p.UpdatedBy)
	}
	if p.UpdatedAt.IsZero() {
		t.Error("updated_at is zero, want set")
	}
}

// --- 导出完整性校验 ---

func TestVerifyExport_DetectsTampering(t *testing.T) {
	svc := newTestService(t)
	mustLog(t, svc, Event{Action: "agent.created", Actor: "a1"})
	mustLog(t, svc, Event{Action: "agent.deleted", Actor: "a2"})

	res, err := svc.Export(context.Background(), Filter{}, FormatCSV)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	ok, reason := VerifyExport(res.Data, res.Manifest)
	if !ok {
		t.Fatalf("VerifyExport = false (%s), want true", reason)
	}

	// 篡改一行内容：条数不变，摘要必须变。
	tampered := strings.Replace(string(res.Data), "agent.deleted", "agent.created", 1)
	if tampered == string(res.Data) {
		t.Fatal("tamper attempt did not change content")
	}
	ok, reason = VerifyExport([]byte(tampered), res.Manifest)
	if ok {
		t.Error("VerifyExport(tampered) = true, want false")
	}
	if reason == "" {
		t.Error("reason is empty for tampered export")
	}
}

func TestVerifyExport_DetectsCountMismatch(t *testing.T) {
	svc := newTestService(t)
	mustLog(t, svc, Event{Action: "agent.created", Actor: "a1"})

	res, err := svc.Export(context.Background(), Filter{}, FormatCSV)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// 声称有 5 条，实际 1 条 —— 必须判为不符，不能只比摘要。
	m := res.Manifest
	m.Count = 5
	ok, _ := VerifyExport(res.Data, m)
	if ok {
		t.Error("VerifyExport with wrong count = true, want false")
	}
}

func TestExport_TruncationIsFlagged(t *testing.T) {
	// 造出超过 maxLimit 的事件量代价太高，这里直接验证判定逻辑：
	// totalMatched > len(records) 时必须打上 truncated 并记录 limit。
	records := []*EventRecord{{Event: Event{Action: "a", Timestamp: time.Now().UTC()}}}
	res, err := buildExport(records, FormatJSON, maxLimit+7, true, maxLimit, time.Now().UTC())
	if err != nil {
		t.Fatalf("buildExport: %v", err)
	}
	if !res.Manifest.Truncated {
		t.Error("manifest.truncated = false, want true")
	}
	if res.Manifest.Limit != maxLimit {
		t.Errorf("manifest.limit = %d, want %d", res.Manifest.Limit, maxLimit)
	}
	if res.Manifest.TotalMatched != maxLimit+7 {
		t.Errorf("manifest.total_matched = %d, want %d", res.Manifest.TotalMatched, maxLimit+7)
	}
	if res.Manifest.Count != 1 {
		t.Errorf("manifest.count = %d, want 1", res.Manifest.Count)
	}
}

func TestExport_TimeRangeCap(t *testing.T) {
	svc := newTestService(t)
	start := time.Now().UTC().AddDate(0, 0, -90)
	end := time.Now().UTC()

	_, err := svc.Export(context.Background(), Filter{StartTime: &start, EndTime: &end}, FormatJSON)
	if err == nil {
		t.Fatal("Export with 90-day window = nil error, want ErrExportRangeTooLarge")
	}
	if !strings.Contains(err.Error(), "time range") {
		t.Errorf("err = %v, want range error", err)
	}
}

func TestParseExportJSON_BackwardCompatible(t *testing.T) {
	// 旧格式（只有 count/records）也必须能解析并重算出摘要，
	// 否则历史归档文件一夜之间失去可校验性。
	legacy := []byte(`{"count":1,"records":[{"id":"e1","action":"user.login","timestamp":"2026-01-01T00:00:00Z","status":"success"}]}`)
	records, manifest, err := parseExportJSON(legacy)
	if err != nil {
		t.Fatalf("parseExportJSON: %v", err)
	}
	if len(records) != 1 || manifest.Count != 1 {
		t.Fatalf("records = %d manifest.count = %d, want 1/1", len(records), manifest.Count)
	}
	if manifest.SHA256 != ComputeDigest(canonicalJSON(records)) {
		t.Error("legacy manifest digest not derivable from content")
	}
	ok, reason := VerifyExport(legacy, manifest)
	if !ok {
		t.Errorf("VerifyExport(legacy) = false (%s), want true", reason)
	}
}

// --- HTTP 层：留存接口 ---

func TestHandler_RetentionEndpoints(t *testing.T) {
	svc := newTestService(t)
	rs := NewRetentionService(svc)
	mux := http.NewServeMux()
	NewHandler(svc).WithRetention(rs).RegisterRoutes(mux)
	srv := newTestServerWithMux(t, mux)

	// 默认策略可读。
	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/retention", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET retention status = %d, body=%s", resp.StatusCode, body)
	}
	var policy RetentionPolicy
	if err := json.Unmarshal(body, &policy); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	if policy.RetentionDays != DefaultRetentionDays {
		t.Errorf("retention_days = %d, want %d", policy.RetentionDays, DefaultRetentionDays)
	}

	// 更新策略。
	resp, body = doJSON(t, http.MethodPut, srv.URL+"/api/v1/audit/retention",
		map[string]interface{}{"enabled": true, "retention_days": 45})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT retention status = %d, body=%s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &policy); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	if policy.RetentionDays != 45 {
		t.Errorf("retention_days = %d, want 45", policy.RetentionDays)
	}

	// 负数必须 400，而不是静默改成默认值。
	resp, _ = doJSON(t, http.MethodPut, srv.URL+"/api/v1/audit/retention",
		map[string]interface{}{"enabled": true, "retention_days": -1})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT retention(-1) status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_RetentionPurgeDefaultsToDryRun(t *testing.T) {
	svc := newTestService(t)
	rs := NewRetentionService(svc)
	if _, err := rs.SetPolicy(RetentionPolicy{Enabled: true, RetentionDays: 1}, "admin"); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	mustLog(t, svc, Event{Action: "agent.created", Timestamp: time.Now().UTC().AddDate(0, 0, -10)})

	mux := http.NewServeMux()
	NewHandler(svc).WithRetention(rs).RegisterRoutes(mux)
	srv := newTestServerWithMux(t, mux)

	// 不传 dry_run：必须按预演处理（默认安全），数据不能被删。
	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/audit/retention/purge", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("purge status = %d, body=%s", resp.StatusCode, body)
	}
	var plan PurgePlan
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}
	if !plan.DryRun || plan.Removed != 0 {
		t.Errorf("plan = %+v, want dry-run with nothing removed", plan)
	}
	if plan.Candidates != 1 {
		t.Errorf("candidates = %d, want 1", plan.Candidates)
	}

	total, _ := svc.Count(context.Background(), Filter{})
	if total != 1 {
		t.Fatalf("count = %d, want 1 (default purge must not delete)", total)
	}

	// 显式 dry_run=false 才真删。
	resp, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/audit/retention/purge",
		map[string]interface{}{"dry_run": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("purge status = %d, body=%s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}
	if plan.DryRun {
		t.Error("plan.DryRun = true, want false when dry_run=false")
	}
	if plan.Removed != 1 {
		t.Errorf("removed = %d, want 1", plan.Removed)
	}
}

func TestHandler_ExportVerifyEndpoint(t *testing.T) {
	srv, svc := newTestServer(t)
	mustLog(t, svc, Event{Action: "agent.created", Actor: "a1"})

	res, err := svc.Export(context.Background(), Filter{}, FormatCSV)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/audit/export/verify",
		map[string]interface{}{
			"manifest": res.Manifest,
			"content":  string(res.Data),
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify status = %d, body=%s", resp.StatusCode, body)
	}
	var out struct {
		Verified bool   `json:"verified"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal verify: %v", err)
	}
	if !out.Verified {
		t.Fatalf("verified = false (%s), want true", out.Reason)
	}

	// 篡改内容 -> verified=false，且接口本身仍返回 200（这是校验结果，不是请求失败）。
	resp, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/audit/export/verify",
		map[string]interface{}{
			"manifest": res.Manifest,
			"content":  string(res.Data) + "injected,row\n",
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify status = %d, want 200 even when verification fails", resp.StatusCode)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal verify: %v", err)
	}
	if out.Verified {
		t.Error("verified = true for tampered content, want false")
	}
	if out.Reason == "" {
		t.Error("reason should explain the failure")
	}

	// content 为空 -> 400。
	resp, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/audit/export/verify",
		map[string]interface{}{"manifest": res.Manifest, "content": ""})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("verify(empty content) status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_RetentionUnconfiguredReturns503(t *testing.T) {
	// 未挂载留存服务时必须是可探测的失败，而不是静默成功。
	srv, _ := newTestServer(t)

	resp, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/retention", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET retention status = %d, want 503", resp.StatusCode)
	}
}
