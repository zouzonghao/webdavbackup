package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/net/websocket"

	"webdav-backup/config"
	"webdav-backup/logger"
	"webdav-backup/scheduler"
	"webdav-backup/webdav"
)

// JWT 密钥，实际应用中应该从配置读取
var jwtSecret = []byte("webdav-backup-secret-key-change-in-production")

// Claims JWT 声明
type Claims struct {
	Username string `json:"username"`
	jwt.RegisteredClaims
}

type Server struct {
	config     *config.Config
	scheduler  *scheduler.Scheduler
	taskFunc   scheduler.TaskFunc
	wsClients  map[*websocket.Conn]bool
	wsMu       sync.Mutex
	logBuffer  []LogEntry
	logMu      sync.Mutex
	httpServer *http.Server
	staticFS   fs.FS
}

type APIResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`    // "history" 或 "realtime"
	BatchID string `json:"batchId,omitempty"` // 批次ID，用于前端识别历史日志批次
}

// LogBatchEnd 日志批次结束标记
type LogBatchEnd struct {
	Type    string `json:"type"`    // "batch_end"
	BatchID string `json:"batchId"` // 批次ID
	Count   int    `json:"count"`   // 批次中日志数量
}

func NewServer(cfg *config.Config, taskFunc scheduler.TaskFunc, staticFS fs.FS) *Server {
	s := &Server{
		config:    cfg,
		taskFunc:  taskFunc,
		wsClients: make(map[*websocket.Conn]bool),
		logBuffer: make([]LogEntry, 0, 100),
		staticFS:  staticFS,
	}
	logger.SetLogCallback(s.broadcastLog)
	return s
}

func (s *Server) broadcastLog(level, msg string) {
	entry := LogEntry{
		Time:    time.Now().Format("2006-01-02 15:04:05"),
		Level:   level,
		Message: msg,
		Type:    "realtime", // 标记为实时日志
	}

	s.logMu.Lock()
	s.logBuffer = append(s.logBuffer, entry)
	if len(s.logBuffer) > 100 {
		s.logBuffer = s.logBuffer[len(s.logBuffer)-100:]
	}
	s.logMu.Unlock()

	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	for client := range s.wsClients {
		websocket.JSON.Send(client, entry)
	}
}

func (s *Server) Start() error {
	s.scheduler = scheduler.NewScheduler(s.taskFunc)
	s.scheduler.Start(s.config)

	mux := http.NewServeMux()

	if s.staticFS != nil {
		fileServer := http.FileServer(http.FS(s.staticFS))
		mux.Handle("/static/", http.StripPrefix("/static/", fileServer))
	}

	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/api/auth", s.handleAuth)
	mux.HandleFunc("/api/tasks", s.authMiddleware(s.handleTasks))
	mux.HandleFunc("/api/tasks/run", s.authMiddleware(s.handleRunAllTasks))
	mux.HandleFunc("/api/tasks/", s.authMiddleware(s.handleTaskRoutes))
	mux.HandleFunc("/api/webdav", s.authMiddleware(s.handleWebDAV))
	mux.HandleFunc("/api/webdav/", s.authMiddleware(s.handleWebDAVRoutes))
	mux.HandleFunc("/api/config", s.authMiddleware(s.handleConfig))
	mux.HandleFunc("/api/status", s.authMiddleware(s.handleStatus))
	mux.HandleFunc("/ws", s.authMiddlewareWS(websocket.Handler(s.handleWebSocket)).ServeHTTP)

	addr := fmt.Sprintf("%s:%d", s.config.WebServer.Host, s.config.WebServer.Port)
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	logger.Info("Web server starting on %s", addr)

	return s.httpServer.ListenAndServe()
}

func (s *Server) Stop() {
	if s.scheduler != nil {
		s.scheduler.Stop()
	}

	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.httpServer.Shutdown(ctx)
	}

	s.wsMu.Lock()
	for client := range s.wsClients {
		client.Close()
	}
	s.wsMu.Unlock()

	logger.Info("Web server stopped")
}

func (s *Server) handleWebSocket(ws *websocket.Conn) {
	s.wsMu.Lock()
	s.wsClients[ws] = true
	s.wsMu.Unlock()

	s.logMu.Lock()
	history := make([]LogEntry, len(s.logBuffer))
	copy(history, s.logBuffer)
	s.logMu.Unlock()

	// 生成批次ID（使用时间戳）
	batchID := time.Now().Format("20060102150405")

	// 正序发送历史日志（从最旧到最新）
	for i := 0; i < len(history); i++ {
		entry := history[i]
		entry.Type = "history" // 标记为历史日志
		entry.BatchID = batchID
		websocket.JSON.Send(ws, entry)
	}

	// 发送批次结束标记
	if len(history) > 0 {
		batchEnd := LogBatchEnd{
			Type:    "batch_end",
			BatchID: batchID,
			Count:   len(history),
		}
		websocket.JSON.Send(ws, batchEnd)
	}

	defer func() {
		s.wsMu.Lock()
		delete(s.wsClients, ws)
		s.wsMu.Unlock()
		ws.Close()
	}()

	var msg string
	for {
		err := websocket.Message.Receive(ws, &msg)
		if err != nil {
			break
		}
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		if s.staticFS != nil {
			http.FileServer(http.FS(s.staticFS)).ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}

	cookie, err := r.Cookie("auth")
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, "/login.html", http.StatusFound)
		return
	}

	if s.staticFS == nil {
		http.Error(w, "Static files not available", http.StatusInternalServerError)
		return
	}

	content, err := fs.ReadFile(s.staticFS, "index.html")
	if err != nil {
		http.Error(w, "Failed to load page", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(content)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if s.staticFS == nil {
			http.Error(w, "Static files not available", http.StatusInternalServerError)
			return
		}
		content, err := fs.ReadFile(s.staticFS, "login.html")
		if err != nil {
			http.Error(w, "Failed to load page", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(content)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: "Invalid request"}, http.StatusBadRequest)
		return
	}

	if creds.Username != s.config.WebServer.Username || creds.Password != s.config.WebServer.Password {
		s.jsonResponse(w, APIResponse{Success: false, Message: "Invalid credentials"}, http.StatusUnauthorized)
		return
	}

	token, err := s.generateToken()
	if err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: "Failed to generate token"}, http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	s.jsonResponse(w, APIResponse{
		Success: true,
		Data:    map[string]interface{}{},
	})
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("auth")
	if err != nil || cookie.Value == "" {
		s.jsonResponse(w, APIResponse{Success: false, Message: "Not authenticated"}, http.StatusUnauthorized)
		return
	}

	s.jsonResponse(w, APIResponse{
		Success: true,
		Data:    map[string]interface{}{},
	})
}

func (s *Server) generateToken() (string, error) {
	claims := &Claims{
		Username: s.config.WebServer.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "webdav-backup",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func (s *Server) validateToken(tokenString string) bool {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtSecret, nil
	})
	if err != nil {
		return false
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims.Username == s.config.WebServer.Username
	}
	return false
}

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("auth")
		if err != nil || cookie.Value == "" {
			s.jsonResponse(w, APIResponse{Success: false, Message: "Not authenticated"}, http.StatusUnauthorized)
			return
		}

		if !s.validateToken(cookie.Value) {
			s.jsonResponse(w, APIResponse{Success: false, Message: "Invalid token"}, http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

func (s *Server) authMiddlewareWS(handler websocket.Handler) websocket.Handler {
	return func(ws *websocket.Conn) {
		req := ws.Request()
		cookie, err := req.Cookie("auth")
		if err != nil || cookie.Value == "" || !s.validateToken(cookie.Value) {
			ws.Close()
			return
		}
		handler(ws)
	}
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listTasks(w, r)
	case http.MethodPost:
		s.createTask(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		s.getTask(w, r, name)
	case http.MethodPut:
		s.updateTask(w, r, name)
	case http.MethodDelete:
		s.deleteTask(w, r, name)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleTaskRoutes 处理 /api/tasks/{name} 和 /api/tasks/{name}/run
func (s *Server) handleTaskRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if path == "" {
		http.Error(w, "Task name required", http.StatusBadRequest)
		return
	}

	// 检查是否是 /run 子路径
	if strings.HasSuffix(path, "/run") {
		name := strings.TrimSuffix(path, "/run")
		if name == "" {
			http.Error(w, "Task name required", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.runTask(w, r, name)
		return
	}

	s.handleTask(w, r, path)
}

// handleRunAllTasks 处理 /api/tasks/run (运行所有任务)
func (s *Server) handleRunAllTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.runAllTasks(w, r)
}

func (s *Server) handleRunTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/api/tasks/run/")
	if name == "" {
		s.runAllTasks(w, r)
		return
	}

	s.runTask(w, r, name)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks := make([]map[string]interface{}, 0)

	for _, task := range s.config.LocalTasks {
		tasks = append(tasks, s.localTaskToMap(&task))
	}

	for _, task := range s.config.NodeImageTasks {
		tasks = append(tasks, s.nodeImageTaskToMap(&task))
	}

	s.jsonResponse(w, APIResponse{Success: true, Data: tasks})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request, name string) {
	task := s.config.GetTaskByName(name)
	if task == nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: "Task not found"}, http.StatusNotFound)
		return
	}

	var taskMap map[string]interface{}
	switch t := task.(type) {
	case *config.LocalBackupTask:
		taskMap = s.localTaskToMap(t)
	case *config.NodeImageSyncTask:
		taskMap = s.nodeImageTaskToMap(t)
	default:
		s.jsonResponse(w, APIResponse{Success: false, Message: "Unknown task type"}, http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, APIResponse{Success: true, Data: taskMap})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string                 `json:"name"`
		Type        string                 `json:"type"`
		Enabled     bool                   `json:"enabled"`
		SyncMode    string                 `json:"sync_mode"`
		Paths       []config.BackupItem    `json:"paths"`
		WebDAV      []string               `json:"webdav"`
		Schedule    config.ScheduleConfig  `json:"schedule"`
		EncryptPwd  string                 `json:"encrypt_pwd"`
		BasePath    string                 `json:"base_path"`
		NodeImage        config.NodeImageConfig `json:"nodeimage"`
		Concurrency      int                    `json:"concurrency"`
		DownloadInterval int                    `json:"download_interval"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusBadRequest)
		return
	}

	if req.Type == "nodeimage" {
		task := &config.NodeImageSyncTask{
			Name:        req.Name,
			Type:        req.Type,
			Enabled:     req.Enabled,
			SyncMode:    req.SyncMode,
			WebDAV:      req.WebDAV,
			Schedule:    req.Schedule,
			NodeImage:        req.NodeImage,
			Concurrency:      req.Concurrency,
			DownloadInterval: req.DownloadInterval,
		}
		s.config.UpdateNodeImageTask(req.Name, task)
		if s.scheduler != nil {
			s.scheduler.AddTask(task, "nodeimage")
		}
	} else {
		task := &config.LocalBackupTask{
			Name:       req.Name,
			Type:       req.Type,
			Enabled:    req.Enabled,
			Paths:      req.Paths,
			WebDAV:     req.WebDAV,
			Schedule:   req.Schedule,
			EncryptPwd: req.EncryptPwd,
			BasePath:   req.BasePath,
		}
		s.config.UpdateLocalTask(req.Name, task)
		if s.scheduler != nil {
			s.scheduler.AddTask(task, "local")
		}
	}

	if err := config.Save(config.GetConfigPath(), s.config); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, APIResponse{Success: true, Message: "Task created"})
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		Name             string                 `json:"name"`
		Type             string                 `json:"type"`
		Enabled          bool                   `json:"enabled"`
		SyncMode         string                 `json:"sync_mode"`
		Paths            []config.BackupItem    `json:"paths"`
		WebDAV           []string               `json:"webdav"`
		Schedule         config.ScheduleConfig  `json:"schedule"`
		EncryptPwd       string                 `json:"encrypt_pwd"`
		BasePath         string                 `json:"base_path"`
		NodeImage        config.NodeImageConfig `json:"nodeimage"`
		Concurrency      int                    `json:"concurrency"`
		DownloadInterval int                    `json:"download_interval"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusBadRequest)
		return
	}

	s.config.DeleteLocalTask(name)
	s.config.DeleteNodeImageTask(name)
	if s.scheduler != nil {
		s.scheduler.RemoveTask(name)
	}

	if req.Type == "nodeimage" {
		task := &config.NodeImageSyncTask{
			Name:             req.Name,
			Type:             req.Type,
			Enabled:          req.Enabled,
			SyncMode:         req.SyncMode,
			WebDAV:           req.WebDAV,
			Schedule:         req.Schedule,
			NodeImage:        req.NodeImage,
			Concurrency:      req.Concurrency,
			DownloadInterval: req.DownloadInterval,
		}
		s.config.UpdateNodeImageTask(req.Name, task)
		if s.scheduler != nil {
			s.scheduler.AddTask(task, "nodeimage")
		}
	} else {
		task := &config.LocalBackupTask{
			Name:       req.Name,
			Type:       req.Type,
			Enabled:    req.Enabled,
			Paths:      req.Paths,
			WebDAV:     req.WebDAV,
			Schedule:   req.Schedule,
			EncryptPwd: req.EncryptPwd,
			BasePath:   req.BasePath,
		}
		s.config.UpdateLocalTask(req.Name, task)
		if s.scheduler != nil {
			s.scheduler.AddTask(task, "local")
		}
	}

	if err := config.Save(config.GetConfigPath(), s.config); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, APIResponse{Success: true, Message: "Task updated"})
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request, name string) {
	s.config.DeleteLocalTask(name)
	s.config.DeleteNodeImageTask(name)
	if s.scheduler != nil {
		s.scheduler.RemoveTask(name)
	}

	if err := config.Save(config.GetConfigPath(), s.config); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, APIResponse{Success: true, Message: "Task deleted"})
}

func (s *Server) runTask(w http.ResponseWriter, r *http.Request, name string) {
	task := s.config.GetTaskByName(name)
	if task == nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: "Task not found"}, http.StatusNotFound)
		return
	}

	go func() {
		if s.scheduler != nil {
			s.scheduler.RunTaskByName(task)
		} else if s.taskFunc != nil {
			s.taskFunc(task)
		}
	}()

	s.jsonResponse(w, APIResponse{Success: true, Message: "Task execution started"})
}

func (s *Server) runAllTasks(w http.ResponseWriter, r *http.Request) {
	go func() {
		for i := range s.config.LocalTasks {
			task := &s.config.LocalTasks[i]
			if task.Enabled && s.taskFunc != nil {
				s.taskFunc(task)
			}
		}
		for i := range s.config.NodeImageTasks {
			task := &s.config.NodeImageTasks[i]
			if task.Enabled && s.taskFunc != nil {
				s.taskFunc(task)
			}
		}
	}()

	s.jsonResponse(w, APIResponse{Success: true, Message: "All tasks execution started"})
}

func (s *Server) handleWebDAV(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listWebDAV(w, r)
	case http.MethodPost:
		s.createWebDAV(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleWebDAVItem(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		s.getWebDAV(w, r, name)
	case http.MethodPut:
		s.updateWebDAV(w, r, name)
	case http.MethodDelete:
		s.deleteWebDAV(w, r, name)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWebDAVRoutes 处理 /api/webdav/{name} 和 /api/webdav/{name}/test
func (s *Server) handleWebDAVRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/webdav/")
	if path == "" {
		http.Error(w, "WebDAV name required", http.StatusBadRequest)
		return
	}

	// 检查是否是 /test 子路径
	if strings.HasSuffix(path, "/test") {
		name := strings.TrimSuffix(path, "/test")
		if name == "" {
			http.Error(w, "WebDAV name required", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.testWebDAV(w, r, name)
		return
	}

	s.handleWebDAVItem(w, r, path)
}

func (s *Server) testWebDAV(w http.ResponseWriter, r *http.Request, name string) {
	wd := s.config.GetWebDAVByName(name)
	if wd == nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: "WebDAV not found"}, http.StatusNotFound)
		return
	}

	client := webdav.NewClient(webdav.Config{
		Name:     wd.Name,
		URL:      wd.URL,
		Username: wd.Username,
		Password: wd.Password,
		Timeout:  wd.Timeout,
	})

	if err := client.TestConnection(); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()})
		return
	}

	s.jsonResponse(w, APIResponse{Success: true, Message: "Connection successful"})
}

func (s *Server) listWebDAV(w http.ResponseWriter, r *http.Request) {
	servers := make([]map[string]interface{}, 0)
	for _, wd := range s.config.WebDAV {
		servers = append(servers, map[string]interface{}{
			"name":     wd.Name,
			"url":      wd.URL,
			"username": wd.Username,
			"timeout":  wd.Timeout,
		})
	}
	s.jsonResponse(w, APIResponse{Success: true, Data: servers})
}

func (s *Server) getWebDAV(w http.ResponseWriter, r *http.Request, name string) {
	wd := s.config.GetWebDAVByName(name)
	if wd == nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: "WebDAV not found"}, http.StatusNotFound)
		return
	}
	s.jsonResponse(w, APIResponse{Success: true, Data: map[string]interface{}{
		"name":     wd.Name,
		"url":      wd.URL,
		"username": wd.Username,
		"timeout":  wd.Timeout,
	}})
}

func (s *Server) createWebDAV(w http.ResponseWriter, r *http.Request) {
	var wd config.WebDAVConfig
	if err := json.NewDecoder(r.Body).Decode(&wd); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusBadRequest)
		return
	}

	s.config.AddWebDAV(&wd)
	if err := config.Save(config.GetConfigPath(), s.config); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, APIResponse{Success: true, Message: "WebDAV added"})
}

func (s *Server) deleteWebDAV(w http.ResponseWriter, r *http.Request, name string) {
	s.config.DeleteWebDAV(name)
	if err := config.Save(config.GetConfigPath(), s.config); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusInternalServerError)
		return
	}
	s.jsonResponse(w, APIResponse{Success: true, Message: "WebDAV deleted"})
}

func (s *Server) updateWebDAV(w http.ResponseWriter, r *http.Request, name string) {
	var wd config.WebDAVConfig
	if err := json.NewDecoder(r.Body).Decode(&wd); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusBadRequest)
		return
	}

	if wd.Password == "" {
		existing := s.config.GetWebDAVByName(name)
		if existing != nil {
			wd.Password = existing.Password
		}
	}

	s.config.DeleteWebDAV(name)
	s.config.AddWebDAV(&wd)
	if err := config.Save(config.GetConfigPath(), s.config); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusInternalServerError)
		return
	}
	s.jsonResponse(w, APIResponse{Success: true, Message: "WebDAV updated"})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.jsonResponse(w, APIResponse{Success: true, Data: map[string]interface{}{
		"webserver": s.config.WebServer,
	}})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg := s.config

	taskStatuses := s.scheduler.GetAllTaskStatus()
	execStatuses := s.scheduler.GetAllExecutionStatus()

	// 创建任务状态映射
	taskMap := make(map[string]*scheduler.TaskStatus)
	for _, ts := range taskStatuses {
		taskMap[ts.Name] = ts
	}

	result := make([]map[string]interface{}, 0)

	// 处理本地备份任务
	for i := range cfg.LocalTasks {
		task := &cfg.LocalTasks[i]
		item := s.buildTaskStatusItem(task.Name, "local", task.Enabled, task.Schedule, taskMap, execStatuses)
		result = append(result, item)
	}

	// 处理 NodeImage 同步任务
	for i := range cfg.NodeImageTasks {
		task := &cfg.NodeImageTasks[i]
		item := s.buildTaskStatusItem(task.Name, "nodeimage", task.Enabled, task.Schedule, taskMap, execStatuses)
		result = append(result, item)
	}

	s.jsonResponse(w, APIResponse{Success: true, Data: result})
}

func (s *Server) buildTaskStatusItem(name, taskType string, enabled bool, schedule config.ScheduleConfig, taskMap map[string]*scheduler.TaskStatus, execStatuses map[string]*scheduler.ExecutionStatus) map[string]interface{} {
	item := map[string]interface{}{
		"name":     name,
		"type":     taskType,
		"enabled":  enabled,
		"schedule": schedule.String(),
	}

	// 从执行状态获取 last_run
	var lastRun time.Time
	if es, ok := execStatuses[name]; ok && es.Status != "running" {
		lastRun = es.EndTime
	}

	if !enabled {
		item["last_run"] = lastRun
		item["next_run"] = "已禁用"
	} else if ts, ok := taskMap[name]; ok {
		// 如果有调度任务信息，优先使用调度器的 lastRun
		if !ts.LastRun.IsZero() {
			item["last_run"] = ts.LastRun
		} else {
			item["last_run"] = lastRun
		}
		item["next_run"] = ts.NextRun
	} else {
		item["last_run"] = lastRun
		item["next_run"] = ""
	}

	if es, ok := execStatuses[name]; ok {
		item["execution"] = map[string]interface{}{
			"status":     es.Status,
			"start_time": es.StartTime,
			"end_time":   es.EndTime,
			"error":      es.Error,
		}
	}

	return item
}

func (s *Server) localTaskToMap(task *config.LocalBackupTask) map[string]interface{} {
	return map[string]interface{}{
		"name":        task.Name,
		"type":        "local",
		"enabled":     task.Enabled,
		"paths":       task.Paths,
		"webdav":      task.WebDAV,
		"schedule":    task.Schedule,
		"encrypt_pwd": task.EncryptPwd,
		"base_path":   task.BasePath,
	}
}

func (s *Server) nodeImageTaskToMap(task *config.NodeImageSyncTask) map[string]interface{} {
	syncMode := task.SyncMode
	if syncMode == "" {
		syncMode = "incremental"
	}
	return map[string]interface{}{
		"name":              task.Name,
		"type":              "nodeimage",
		"enabled":           task.Enabled,
		"sync_mode":         syncMode,
		"webdav":            task.WebDAV,
		"schedule":          task.Schedule,
		"concurrency":       task.Concurrency,
		"download_interval": task.DownloadInterval,
		"nodeimage":         task.NodeImage,
	}
}

func (s *Server) jsonResponse(w http.ResponseWriter, resp APIResponse, statusCode ...int) {
	w.Header().Set("Content-Type", "application/json")
	if len(statusCode) > 0 {
		w.WriteHeader(statusCode[0])
	}
	json.NewEncoder(w).Encode(resp)
}
