package cloud

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Handler 云环境 HTTP 处理器
type Handler struct {
	service *Service
}

// NewHandler 创建云环境处理器
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/environments", h.handleEnvironments)
	mux.HandleFunc("/api/v1/environments/", h.handleEnvironmentByID)
}

// handleEnvironments 处理 /api/v1/environments
func (h *Handler) handleEnvironments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.List(w, r)
	case http.MethodPost:
		h.Create(w, r)
	default:
		writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleEnvironmentByID 处理 /api/v1/environments/{id}
func (h *Handler) handleEnvironmentByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/environments/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		writeCloudError(w, http.StatusBadRequest, "environment id is required")
		return
	}

	id := parts[0]

	// 处理子路由: /api/v1/environments/{id}/{action}
	if len(parts) > 1 {
		action := parts[1]
		h.handleAction(w, r, id, action)
		return
	}

	// 处理 CRUD 路由
	switch r.Method {
	case http.MethodGet:
		h.Get(w, r, id)
	case http.MethodDelete:
		h.Delete(w, r, id)
	default:
		writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAction 处理环境操作
func (h *Handler) handleAction(w http.ResponseWriter, r *http.Request, envID, action string) {
	if r.Method != http.MethodPost {
		writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()
	var err error

	switch action {
	case "start":
		err = h.service.StartEnvironment(ctx, envID)
	case "stop":
		err = h.service.StopEnvironment(ctx, envID)
	case "pause":
		err = h.service.PauseEnvironment(ctx, envID)
	case "resume":
		err = h.service.ResumeEnvironment(ctx, envID)
	case "exec":
		h.ExecuteCommand(w, r, envID)
		return
	case "upload":
		h.UploadFile(w, r, envID)
		return
	case "download":
		h.DownloadFile(w, r, envID)
		return
	case "files":
		h.ListFiles(w, r, envID)
		return
	case "metrics":
		h.GetMetrics(w, r, envID)
		return
	default:
		writeCloudError(w, http.StatusBadRequest, "unknown action: "+action)
		return
	}

	if err != nil {
		writeCloudError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeCloudJSON(w, http.StatusOK, map[string]string{
		"message": action + " environment successfully",
	})
}

// Create 创建环境
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string          `json:"agent_id"`
		Config  CreateEnvRequest `json:"config"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCloudError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.AgentID == "" {
		writeCloudError(w, http.StatusBadRequest, "agent_id is required")
		return
	}

	env, err := h.service.CreateEnvironment(r.Context(), req.AgentID, req.Config)
	if err != nil {
		writeCloudError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeCloudJSON(w, http.StatusCreated, ToResponse(env))
}

// Get 获取环境详情
func (h *Handler) Get(w http.ResponseWriter, r *http.Request, envID string) {
	env, err := h.service.GetEnvironment(r.Context(), envID)
	if err != nil {
		writeCloudError(w, http.StatusNotFound, err.Error())
		return
	}

	writeCloudJSON(w, http.StatusOK, ToResponse(env))
}

// Delete 删除环境
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request, envID string) {
	if err := h.service.DestroyEnvironment(r.Context(), envID); err != nil {
		writeCloudError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeCloudJSON(w, http.StatusOK, map[string]string{
		"message": "environment deleted successfully",
	})
}

// List 列出环境
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		writeCloudError(w, http.StatusBadRequest, "agent_id query parameter is required")
		return
	}

	envs, err := h.service.ListEnvironments(r.Context(), agentID)
	if err != nil {
		writeCloudError(w, http.StatusInternalServerError, err.Error())
		return
	}

	responses := make([]EnvResponse, len(envs))
	for i, env := range envs {
		responses[i] = ToResponse(env)
	}

	writeCloudJSON(w, http.StatusOK, EnvListResponse{
		Environments: responses,
		Total:        len(responses),
	})
}

// ExecuteCommand 执行命令
func (h *Handler) ExecuteCommand(w http.ResponseWriter, r *http.Request, envID string) {
	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCloudError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result, err := h.service.ExecuteCommand(r.Context(), envID, req)
	if err != nil {
		writeCloudError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeCloudJSON(w, http.StatusOK, result)
}

// UploadFile 上传文件
func (h *Handler) UploadFile(w http.ResponseWriter, r *http.Request, envID string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeCloudError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	// 读取请求体作为文件内容
	var buf []byte
	if r.Body != nil {
		defer r.Body.Close()
		buf = make([]byte, r.ContentLength)
		n, _ := r.Body.Read(buf)
		buf = buf[:n]
	}

	if err := h.service.UploadFile(r.Context(), envID, path, buf); err != nil {
		writeCloudError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeCloudJSON(w, http.StatusOK, map[string]string{
		"message": fmt.Sprintf("file uploaded to %s", path),
	})
}

// DownloadFile 下载文件
func (h *Handler) DownloadFile(w http.ResponseWriter, r *http.Request, envID string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeCloudError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	content, err := h.service.DownloadFile(r.Context(), envID, path)
	if err != nil {
		writeCloudError(w, http.StatusNotFound, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", path))
	w.WriteHeader(http.StatusOK)
	w.Write(content)
}

// ListFiles 列出文件
func (h *Handler) ListFiles(w http.ResponseWriter, r *http.Request, envID string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}

	files, err := h.service.ListFiles(r.Context(), envID, path)
	if err != nil {
		writeCloudError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeCloudJSON(w, http.StatusOK, map[string]interface{}{
		"path":  path,
		"files": files,
	})
}

// GetMetrics 获取资源指标
func (h *Handler) GetMetrics(w http.ResponseWriter, r *http.Request, envID string) {
	metrics, err := h.service.GetMetrics(r.Context(), envID)
	if err != nil {
		writeCloudError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeCloudJSON(w, http.StatusOK, metrics)
}

// writeCloudJSON 写入 JSON 响应
func writeCloudJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// writeCloudError 写入错误响应
func writeCloudError(w http.ResponseWriter, status int, message string) {
	writeCloudJSON(w, status, map[string]string{"error": message})
}
