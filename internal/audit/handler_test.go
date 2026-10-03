package audit

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer 创建挂载了审计路由的测试服务器。
func newTestServer(t *testing.T) (*httptest.Server, Service) {
	t.Helper()
	svc := NewService(NewMemoryRepository())
	mux := http.NewServeMux()
	NewHandler(svc).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, svc
}

func doJSON(t *testing.T, method, url string, body interface{}) (*http.Response, []byte) {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, buf.Bytes()
}

// readExportParts 从 multipart/mixed 导出响应里取出「产物」与「manifest」两部分。
//
// 刻意不复用 HTTP 头里的摘要：校验必须对产物本身做，头可以被中间层改写。
func readExportParts(t *testing.T, contentType string, body []byte) ([]byte, ExportManifest) {
	t.Helper()

	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse media type: %v", err)
	}
	boundary := params["boundary"]
	if boundary == "" {
		t.Fatalf("no boundary in content-type %q", contentType)
	}

	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	var product []byte
	var manifest ExportManifest
	found := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		raw, err := io.ReadAll(part)
		part.Close()
		if err != nil {
			t.Fatalf("read part body: %v", err)
		}
		found++
		if part.FormName() == "" && part.FileName() == ExportManifestPart {
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatalf("unmarshal manifest: %v\n%s", err, raw)
			}
			continue
		}
		product = raw
	}
	if found != 2 {
		t.Fatalf("multipart parts = %d, want 2", found)
	}
	if product == nil {
		t.Fatal("product part not found")
	}
	return product, manifest
}

func TestHandler_CreateAndListEvents(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/audit/events", map[string]interface{}{
		"action":     "user.login",
		"actor":      "u1",
		"actor_type": ActorTypeUser,
		"resource":   ResourceUser,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", resp.StatusCode, body)
	}

	var created EventRecord
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal created: %v", err)
	}
	if created.ID == "" {
		t.Error("expected generated ID in response")
	}
	if created.Action != "user.login" {
		t.Errorf("action = %q, want user.login", created.Action)
	}

	resp, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/events", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}

	var list struct {
		Total   int            `json:"total"`
		Events  []*EventRecord `json:"events"`
		HasMore bool           `json:"has_more"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if list.Total != 1 || len(list.Events) != 1 {
		t.Fatalf("list = %+v, want 1 event", list)
	}
	if list.HasMore {
		t.Error("has_more = true, want false")
	}
}

func TestHandler_CreateEvent_InvalidBody(t *testing.T) {
	srv, _ := newTestServer(t)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/audit/events", strings.NewReader("{not json"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_CreateEvent_MissingAction(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/audit/events", map[string]interface{}{
		"actor": "u1",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "action") {
		t.Errorf("body = %s, want to mention action", body)
	}
}

func TestHandler_GetEventByID(t *testing.T) {
	srv, svc := newTestServer(t)

	rec := mustLog(t, svc, Event{Action: "agent.created", Actor: "a1"})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/events/"+rec.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}

	var got EventRecord
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != rec.ID {
		t.Errorf("id = %q, want %q", got.ID, rec.ID)
	}

	resp, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/events/does-not-exist", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandler_FiltersAndPagination(t *testing.T) {
	srv, svc := newTestServer(t)

	for i := 0; i < 5; i++ {
		mustLog(t, svc, Event{Action: "agent.updated", Actor: "a1", ActorType: ActorTypeAgent})
	}
	mustLog(t, svc, Event{Action: "user.login", Actor: "u1"})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/events?actor_type=agent&limit=2", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}

	var list struct {
		Total   int            `json:"total"`
		Events  []*EventRecord `json:"events"`
		HasMore bool           `json:"has_more"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if list.Total != 5 {
		t.Errorf("total = %d, want 5 (unpaged count)", list.Total)
	}
	if len(list.Events) != 2 {
		t.Errorf("len(events) = %d, want 2", len(list.Events))
	}
	if !list.HasMore {
		t.Error("has_more = false, want true")
	}
}

func TestHandler_FilterByUnixTime(t *testing.T) {
	srv, svc := newTestServer(t)

	mustLog(t, svc, Event{Action: "tick"})

	// Unix 秒写法应被接受，且范围覆盖刚写入的事件
	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/events?start_time=0&end_time=4102444800", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"total":1`) {
		t.Errorf("body = %s, want total 1", body)
	}
}

func TestHandler_InvalidTimeReturns400(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/events?start_time=yesterday", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_Stats(t *testing.T) {
	srv, svc := newTestServer(t)

	mustLog(t, svc, Event{Action: "agent.created", Actor: "a1"})
	mustLog(t, svc, Event{Action: "agent.created", Actor: "a2"})
	mustLog(t, svc, Event{Action: "user.login", Actor: "u1"})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/stats?dimension=action", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}

	var out struct {
		Dimension string         `json:"dimension"`
		Total     int            `json:"total"`
		Buckets   map[string]int `json:"buckets"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Dimension != "action" || out.Total != 3 {
		t.Fatalf("out = %+v, want dimension=action total=3", out)
	}
	if out.Buckets["agent.created"] != 2 {
		t.Errorf("agent.created = %d, want 2", out.Buckets["agent.created"])
	}
}

func TestHandler_Stats_UnknownDimension(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/stats?dimension=nope", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_Distinct(t *testing.T) {
	srv, svc := newTestServer(t)

	mustLog(t, svc, Event{Action: "user.login"})
	mustLog(t, svc, Event{Action: "agent.created"})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/distinct?field=action", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}

	var out struct {
		Field  string   `json:"field"`
		Values []string `json:"values"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Values) != 2 {
		t.Fatalf("values = %v, want 2", out.Values)
	}
	if out.Values[0] != "agent.created" {
		t.Errorf("values[0] = %q, want agent.created (sorted)", out.Values[0])
	}

	resp, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/distinct", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when field missing", resp.StatusCode)
	}
}

func TestHandler_ExportJSON(t *testing.T) {
	srv, svc := newTestServer(t)

	mustLog(t, svc, Event{Action: "agent.created", Actor: "a1"})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/export?format=json", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}
	// 产物与 manifest 同一份 multipart 下发，manifest 不能只存在于响应头里。
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "multipart/mixed") {
		t.Fatalf("content-type = %q, want multipart/mixed", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("content-disposition = %q, want attachment", cd)
	}
	// 摘要与条数同时出现在响应头，便于不解析 body 就做快速核对。
	if resp.Header.Get("X-Audit-SHA256") == "" {
		t.Error("missing X-Audit-SHA256 header")
	}
	if got := resp.Header.Get("X-Audit-Count"); got != "1" {
		t.Errorf("X-Audit-Count = %q, want 1", got)
	}

	product, manifest := readExportParts(t, resp.Header.Get("Content-Type"), body)
	_ = product

	if manifest.Count != 1 {
		t.Fatalf("manifest.count = %d, want 1", manifest.Count)
	}
	if manifest.SHA256 == "" {
		t.Error("manifest.sha256 is empty")
	}
	if manifest.Format != FormatJSON {
		t.Errorf("manifest.format = %q, want json", manifest.Format)
	}
	// 校验接口必须能把「产物 + manifest」判为通过。
	ok, reason := svc.VerifyExport(product, manifest)
	if !ok {
		t.Fatalf("VerifyExport = false (%s), want true", reason)
	}
}

func TestHandler_ExportCSV(t *testing.T) {
	srv, svc := newTestServer(t)

	mustLog(t, svc, Event{Action: "agent.created", Actor: "a1"})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/export?format=csv", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "multipart/mixed") {
		t.Fatalf("content-type = %q, want multipart/mixed", ct)
	}

	product, manifest := readExportParts(t, resp.Header.Get("Content-Type"), body)
	if !strings.Contains(string(product), "id,timestamp,actor") {
		t.Errorf("product = %s, want csv header", product)
	}
	ok, reason := svc.VerifyExport(product, manifest)
	if !ok {
		t.Fatalf("VerifyExport = false (%s), want true", reason)
	}
}

func TestHandler_ExportUnsupportedFormat(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/export?format=xlsx", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_Purge(t *testing.T) {
	srv, svc := newTestServer(t)

	mustLog(t, svc, Event{Action: "tick"})

	resp, body := doJSON(t, http.MethodDelete, srv.URL+"/api/v1/audit/purge?before=2999-01-01T00:00:00Z", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}

	var out struct {
		Removed int `json:"removed"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Removed != 1 {
		t.Fatalf("removed = %d, want 1", out.Removed)
	}
}

func TestHandler_PurgeInvalidTime(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, _ := doJSON(t, http.MethodDelete, srv.URL+"/api/v1/audit/purge?before=not-a-time", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/api/v1/audit/events"},
		{http.MethodDelete, "/api/v1/audit/events"},
		{http.MethodPost, "/api/v1/audit/stats"},
		{http.MethodPost, "/api/v1/audit/export"},
	}
	for _, tc := range cases {
		resp, _ := doJSON(t, tc.method, srv.URL+tc.path, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s status = %d, want 405", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestHandler_EmptyListReturnsEmptyArray(t *testing.T) {
	srv, _ := newTestServer(t)

	_, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit/events", nil)
	if !strings.Contains(string(body), `"events":[]`) {
		t.Errorf("body = %s, want empty array ([] not null)", body)
	}
}
