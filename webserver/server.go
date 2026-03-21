package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/websocket"

	"webdav-backup/config"
	"webdav-backup/logger"
	"webdav-backup/scheduler"
	"webdav-backup/webdav"
)

type Server struct {
	config     *config.Config
	scheduler  *scheduler.Scheduler
	taskFunc   scheduler.TaskFunc
	wsClients  map[*websocket.Conn]bool
	wsMu       sync.Mutex
	logBuffer  []LogEntry
	logMu      sync.Mutex
	httpServer *http.Server
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
}

func NewServer(cfg *config.Config, taskFunc scheduler.TaskFunc) *Server {
	s := &Server{
		config:    cfg,
		taskFunc:  taskFunc,
		wsClients: make(map[*websocket.Conn]bool),
		logBuffer: make([]LogEntry, 0, 100),
	}
	logger.SetLogCallback(s.broadcastLog)
	return s
}

func (s *Server) broadcastLog(level, msg string) {
	entry := LogEntry{
		Time:    time.Now().Format("2006-01-02 15:04:05"),
		Level:   level,
		Message: msg,
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
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/api/auth", s.handleAuth)
	mux.HandleFunc("/api/tasks", s.authMiddleware(s.handleTasks))
	mux.HandleFunc("/api/tasks/", s.authMiddleware(s.handleTask))
	mux.HandleFunc("/api/tasks/run/", s.authMiddleware(s.handleRunTask))
	mux.HandleFunc("/api/webdav", s.authMiddleware(s.handleWebDAV))
	mux.HandleFunc("/api/webdav/test/", s.authMiddleware(s.handleWebDAVTest))
	mux.HandleFunc("/api/webdav/", s.authMiddleware(s.handleWebDAVItem))
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

	for i := len(history) - 1; i >= 0; i-- {
		websocket.JSON.Send(ws, history[i])
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
		http.NotFound(w, r)
		return
	}

	cookie, err := r.Cookie("auth")
	if err != nil || cookie.Value == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(loginHTML))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
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
		SameSite: http.SameSiteStrictMode,
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
	hash, err := bcrypt.GenerateFromPassword([]byte(s.config.WebServer.Password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func (s *Server) validateToken(token string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(token), []byte(s.config.WebServer.Password))
	return err == nil
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

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if name == "" {
		http.Error(w, "Task name required", http.StatusBadRequest)
		return
	}

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
	for _, task := range s.config.Tasks {
		tasks = append(tasks, s.taskToMap(&task))
	}
	s.jsonResponse(w, APIResponse{Success: true, Data: tasks})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request, name string) {
	task := s.config.GetTaskByName(name)
	if task == nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: "Task not found"}, http.StatusNotFound)
		return
	}
	s.jsonResponse(w, APIResponse{Success: true, Data: s.taskToMap(task)})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var task config.BackupTask
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusBadRequest)
		return
	}

	s.config.UpdateTask(task.Name, &task)
	if err := config.Save(config.GetConfigPath(), s.config); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusInternalServerError)
		return
	}

	if task.Enabled && s.scheduler != nil {
		s.scheduler.AddTask(&task)
	}

	s.jsonResponse(w, APIResponse{Success: true, Message: "Task created", Data: s.taskToMap(&task)})
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request, name string) {
	var task config.BackupTask
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusBadRequest)
		return
	}

	if name != task.Name {
		s.config.DeleteTask(name)
		if s.scheduler != nil {
			s.scheduler.RemoveTask(name)
		}
	}

	s.config.UpdateTask(task.Name, &task)
	if err := config.Save(config.GetConfigPath(), s.config); err != nil {
		s.jsonResponse(w, APIResponse{Success: false, Message: err.Error()}, http.StatusInternalServerError)
		return
	}

	if s.scheduler != nil {
		if task.Enabled {
			s.scheduler.AddTask(&task)
		} else {
			s.scheduler.RemoveTask(task.Name)
		}
	}

	s.jsonResponse(w, APIResponse{Success: true, Message: "Task updated", Data: s.taskToMap(&task)})
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request, name string) {
	s.config.DeleteTask(name)
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
		for i := range s.config.Tasks {
			task := &s.config.Tasks[i]
			if task.Enabled {
				if s.scheduler != nil {
					s.scheduler.RunTaskByName(task)
				} else if s.taskFunc != nil {
					s.taskFunc(task)
				}
			}
		}
	}()

	s.jsonResponse(w, APIResponse{Success: true, Message: "All enabled tasks started"})
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

func (s *Server) handleWebDAVItem(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/webdav/")
	if name == "" {
		http.Error(w, "WebDAV name required", http.StatusBadRequest)
		return
	}

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

func (s *Server) handleWebDAVTest(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/webdav/test/")
	if name == "" {
		http.Error(w, "WebDAV name required", http.StatusBadRequest)
		return
	}

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

	taskStatuses := s.scheduler.GetAllTaskStatus()
	execStatuses := s.scheduler.GetAllExecutionStatus()

	result := make([]map[string]interface{}, 0, len(taskStatuses))
	for _, ts := range taskStatuses {
		item := map[string]interface{}{
			"name":     ts.Name,
			"enabled":  ts.Enabled,
			"schedule": ts.Schedule,
			"last_run": ts.LastRun,
			"next_run": ts.NextRun,
		}
		if es, ok := execStatuses[ts.Name]; ok {
			item["execution"] = map[string]interface{}{
				"status":     es.Status,
				"start_time": es.StartTime,
				"end_time":   es.EndTime,
				"error":      es.Error,
			}
		}
		result = append(result, item)
	}

	s.jsonResponse(w, APIResponse{Success: true, Data: result})
}

func (s *Server) taskToMap(task *config.BackupTask) map[string]interface{} {
	return map[string]interface{}{
		"name":        task.Name,
		"enabled":     task.Enabled,
		"paths":       task.Paths,
		"webdav":      task.WebDAV,
		"schedule":    task.Schedule,
		"encrypt_pwd": task.EncryptPwd,
	}
}

func (s *Server) jsonResponse(w http.ResponseWriter, resp APIResponse, statusCode ...int) {
	w.Header().Set("Content-Type", "application/json")
	if len(statusCode) > 0 {
		w.WriteHeader(statusCode[0])
	}
	json.NewEncoder(w).Encode(resp)
}

var loginHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>备份管理器 - 登录</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:system-ui,-apple-system,sans-serif;background:#f5f5f5;color:#333;min-height:100vh;display:flex;align-items:center;justify-content:center}
.login-box{background:#fff;padding:2rem;border-radius:8px;width:320px;box-shadow:0 2px 8px rgba(0,0,0,.1)}
h1{font-size:1.25rem;font-weight:600;margin-bottom:1.5rem;color:#2563eb;text-align:center}
.form-group{margin-bottom:1rem}
.form-group label{display:block;font-size:0.75rem;color:#666;margin-bottom:0.25rem}
.form-group input{width:100%;padding:0.75rem;background:#f9fafb;border:1px solid #e5e7eb;border-radius:4px;color:#333;font-size:0.875rem}
.form-group input:focus{outline:none;border-color:#2563eb}
.btn{width:100%;background:#2563eb;color:#fff;border:none;padding:0.75rem;border-radius:4px;cursor:pointer;font-size:0.875rem;font-weight:500;transition:background .2s}
.btn:hover{background:#1d4ed8}
.error{color:#dc2626;font-size:0.75rem;margin-top:0.5rem;text-align:center}
</style>
</head>
<body>
<div class="login-box">
<h1>备份管理器</h1>
<form id="login-form">
<div class="form-group"><label>用户名</label><input type="text" id="username" required autocomplete="username"></div>
<div class="form-group"><label>密码</label><input type="password" id="password" required autocomplete="current-password"></div>
<button type="submit" class="btn">登录</button>
<div id="error" class="error"></div>
</form>
</div>
<script>
document.getElementById('login-form').onsubmit=async function(e){
e.preventDefault();
const username=document.getElementById('username').value;
const password=document.getElementById('password').value;
try{
const r=await fetch('/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username,password})});
const d=await r.json();
if(d.success){
location.reload();
}else{
document.getElementById('error').textContent=d.message||'登录失败';
}
}catch(err){
document.getElementById('error').textContent='连接错误';
}
};
</script>
</body>
</html>`

var indexHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>备份管理器</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:system-ui,-apple-system,sans-serif;background:#f5f5f5;color:#333;min-height:100vh}
.container{max-width:1200px;margin:0 auto;padding:20px}
h1{font-size:1.5rem;font-weight:600;margin-bottom:1rem;color:#2563eb}
.card{background:#fff;border-radius:8px;padding:1rem;margin-bottom:1rem;box-shadow:0 1px 3px rgba(0,0,0,.1)}
.card h2{font-size:1rem;font-weight:500;margin-bottom:0.75rem;color:#2563eb;border-bottom:1px solid #e5e7eb;padding-bottom:0.5rem}
.btn{background:#2563eb;color:#fff;border:none;padding:0.5rem 1rem;border-radius:4px;cursor:pointer;font-size:0.875rem;font-weight:500;transition:background .2s}
.btn:hover{background:#1d4ed8}
.btn.danger{background:#dc2626}
.btn.danger:hover{background:#b91c1c}
.btn.small{padding:0.25rem 0.5rem;font-size:0.75rem}
table{width:100%;border-collapse:collapse;table-layout:fixed}
th,td{text-align:left;padding:0.5rem;border-bottom:1px solid #e5e7eb}
th{color:#666;font-weight:500;font-size:0.75rem;text-transform:uppercase;background:#f9fafb}
.status{display:inline-block;padding:0.125rem 0.5rem;border-radius:4px;font-size:0.75rem}
.status.enabled{background:#dcfce7;color:#166534}
.status.disabled{background:#fee2e2;color:#991b1b}
.form-group{margin-bottom:0.75rem}
.form-group label{display:block;font-size:0.75rem;color:#666;margin-bottom:0.25rem}
.form-group input:not([type=checkbox]):not([type=radio]),.form-group select,.form-group textarea{width:100%;padding:0.5rem;background:#f9fafb;border:1px solid #e5e7eb;border-radius:4px;color:#333;font-size:0.875rem}
.form-group input[type=checkbox],.form-group input[type=radio]{width:auto;margin-right:0.25rem}
.form-group input:focus,.form-group select:focus,.form-group textarea:focus{outline:none;border-color:#2563eb}
.form-row{display:grid;grid-template-columns:1fr 1fr;gap:0.5rem}
.modal{display:none;position:fixed;top:0;left:0;width:100%;height:100%;background:rgba(0,0,0,.5);align-items:center;justify-content:center;z-index:100}
.modal.show{display:flex}
.modal-content{background:#fff;padding:1.5rem;border-radius:8px;max-width:650px;width:90%;max-height:90vh;overflow-y:auto;box-shadow:0 4px 12px rgba(0,0,0,.15)}
.modal-header{display:flex;justify-content:space-between;align-items:center;margin-bottom:1rem}
.modal-header h3{font-size:1rem;font-weight:500;color:#2563eb}
.close{background:none;border:none;color:#999;font-size:1.5rem;cursor:pointer}
.close:hover{color:#333}
.tabs{display:flex;gap:0.5rem;margin-bottom:1rem}
.tab{padding:0.5rem 1rem;background:#fff;border:1px solid #e5e7eb;border-radius:4px;color:#666;cursor:pointer;font-size:0.875rem}
.tab:hover{background:#f9fafb}
.tab.active{background:#2563eb;color:#fff;border-color:#2563eb}
.hidden{display:none}
.alert{background:#fef3c7;color:#92400e;padding:1rem;border-radius:4px;margin-bottom:1rem;border:1px solid #fcd34d}
.alert h3{margin-bottom:0.5rem}
.alert input{width:100%;padding:0.5rem;background:#fff;border:1px solid #e5e7eb;border-radius:4px;color:#333;margin-top:0.5rem}
.alert .btn{margin-top:0.5rem;background:#d97706}
.alert .btn:hover{background:#b45309}
.log-container{background:#1e293b;border-radius:4px;padding:0.5rem;height:500px;overflow-y:auto;font-family:monospace;font-size:0.75rem}
.log-line{padding:0.125rem 0}
.log-line.INFO{color:#4ade80}
.log-line.ERROR{color:#f87171}
.log-line.WARN{color:#fbbf24}
.log-line.DEBUG{color:#94a3b8}
.log-time{color:#64748b;margin-right:0.5rem}
</style>
</head>
<body>
<div class="container">
<h1>备份管理器</h1>
<div class="tabs">
<button class="tab active" onclick="showTab('tasks')">任务</button>
<button class="tab" onclick="showTab('webdav')">WebDAV</button>
<button class="tab" onclick="showTab('status')">状态</button>
<button class="tab" onclick="showTab('logs')">日志</button>
</div>
<div id="tasks-tab">
<div class="card">
<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:1rem">
<h2 style="border:none;margin:0">备份任务</h2>
<button class="btn" onclick="showTaskModal()">添加任务</button>
</div>
<table>
<thead><tr><th>名称</th><th>计划</th><th>状态</th><th>操作</th></tr></thead>
<tbody id="tasks-list"></tbody>
</table>
</div>
</div>
<div id="webdav-tab" class="hidden">
<div class="card">
<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:1rem">
<h2 style="border:none;margin:0">WebDAV 服务器</h2>
<button class="btn" onclick="showWebdavModal()">添加服务器</button>
</div>
<table>
<thead><tr><th>名称</th><th>地址</th><th>操作</th></tr></thead>
<tbody id="webdav-list"></tbody>
</table>
</div>
</div>
<div id="status-tab" class="hidden">
<div class="card">
<h2>任务状态</h2>
<table>
<thead><tr><th>任务</th><th>上次运行</th><th>下次运行</th></tr></thead>
<tbody id="status-list"></tbody>
</table>
</div>
</div>
<div id="logs-tab" class="hidden">
<div class="card">
<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:1rem">
<h2 style="margin:0">实时日志</h2>
<button class="btn small" onclick="clearLogs()">清屏</button>
</div>
<div class="log-container" id="log-output"></div>
</div>
</div>
</div>
</div>
<div class="modal" id="task-modal">
<div class="modal-content">
<div class="modal-header">
<h3 id="modal-title">添加任务</h3>
<button class="close" onclick="closeModal('task-modal')">&times;</button>
</div>
<form id="task-form">
<input type="hidden" id="task-original-name">
<div class="form-row">
<div class="form-group"><label>名称</label><input type="text" id="task-name" required></div>
<div class="form-group"><label>启用</label><select id="task-enabled"><option value="true">是</option><option value="false">否</option></select></div>
</div>
<div class="form-group"><label>备份路径（每行一个，文件或目录路径）</label><textarea id="task-paths" style="width:100%;height:150px;background:#f9fafb;border:1px solid #e5e7eb;border-radius:4px;color:#333;padding:0.5rem" placeholder="/path/to/backup&#10;/another/path"></textarea></div>
<div class="form-group"><label>WebDAV 服务器</label><div id="webdav-checkboxes" style="background:#f9fafb;border:1px solid #e5e7eb;border-radius:4px;padding:0.5rem;min-height:40px;max-height:120px;overflow-y:auto"></div></div>
<div class="form-row">
<div class="form-group"><label>计划类型</label><select id="task-schedule-type" onchange="updateScheduleFields()"><option value="hourly">每小时</option><option value="daily" selected>每天</option><option value="weekly">每周</option></select></div>
<div class="form-group" id="hour-group"><label>小时</label><input type="number" id="task-hour" value="0" min="0" max="23"></div>
</div>
<div class="form-row">
<div class="form-group" id="day-group" style="display:none"><label>星期</label><select id="task-day"><option value="0">周日</option><option value="1">周一</option><option value="2">周二</option><option value="3">周三</option><option value="4">周四</option><option value="5">周五</option><option value="6">周六</option></select></div>
<div class="form-group"><label>分钟</label><input type="number" id="task-minute" value="0" min="0" max="59"></div>
</div>
<div class="form-group"><label>加密密码（可选，用于 AES-256 加密）</label><input type="text" id="task-encrypt-pwd" placeholder="留空则不加密"></div>
<div style="display:flex;gap:0.5rem;justify-content:flex-end;margin-top:1rem">
<button type="button" class="btn danger" onclick="closeModal('task-modal')">取消</button>
<button type="submit" class="btn">保存</button>
</div>
</form>
</div>
</div>
<div class="modal" id="webdav-modal">
<div class="modal-content">
<div class="modal-header">
<h3 id="webdav-modal-title">添加 WebDAV 服务器</h3>
<button class="close" onclick="closeModal('webdav-modal')">&times;</button>
</div>
<form id="webdav-form">
<input type="hidden" id="webdav-original-name">
<div class="form-group"><label>名称</label><input type="text" id="webdav-name" required></div>
<div class="form-group"><label>地址</label><input type="url" id="webdav-url" required placeholder="https://dav.example.com"></div>
<div class="form-group"><label>用户名</label><input type="text" id="webdav-username"></div>
<div class="form-group"><label>密码</label><input type="password" id="webdav-password" placeholder="留空则不修改"></div>
<div class="form-group"><label>超时时间（秒）</label><input type="number" id="webdav-timeout" value="300"></div>
<div style="display:flex;gap:0.5rem;justify-content:flex-end;margin-top:1rem">
<button type="button" class="btn danger" onclick="closeModal('webdav-modal')">取消</button>
<button type="submit" class="btn">保存</button>
</div>
</form>
</div>
</div>
<script>
let ws;
function connectWS(){
const loc=window.location;
ws=new WebSocket((loc.protocol==='https:'?'wss:':'ws:')+'//'+loc.host+'/ws');
ws.onmessage=function(e){
const d=JSON.parse(e.data);
addLog(d.time,d.Level||d.level,d.Message||d.message);
};
ws.onclose=function(){setTimeout(connectWS,3000)};
}
function addLog(time,level,msg){
const line=document.createElement('div');
line.className='log-line '+(level||'INFO');
line.innerHTML='<span class="log-time">'+time+'</span>'+msg;
const el=document.getElementById('log-output');
el.insertBefore(line,el.firstChild);
}
function api(m,u,d){return fetch(u,{method:m,headers:{'Content-Type':'application/json'},body:d?JSON.stringify(d):null}).then(r=>r.json())}
function loadTasks(){api('GET','/api/tasks').then(r=>{const t=document.getElementById('tasks-list');t.innerHTML='';(r.data||[]).forEach(x=>{const sched=x.schedule.type==='hourly'?'每小时 :'+String(x.schedule.minute||0).padStart(2,'0'):x.schedule.type==='daily'?'每天 '+(x.schedule.hour||0)+':'+String(x.schedule.minute||0).padStart(2,'0'):'每周 '+['日','一','二','三','四','五','六'][x.schedule.day||0]+' '+(x.schedule.hour||0)+':'+String(x.schedule.minute||0).padStart(2,'0');t.innerHTML+='<tr><td>'+x.name+'</td><td>'+sched+'</td><td><span class="status '+(x.enabled?'enabled':'disabled')+'">'+(x.enabled?'已启用':'已禁用')+'</span></td><td><button class="btn small" onclick="runTask(\''+x.name+'\')">运行</button> <button class="btn small" onclick="editTask(\''+x.name+'\')">编辑</button> <button class="btn small danger" onclick="deleteTask(\''+x.name+'\')">删除</button></td></tr>'})})}
function loadWebdav(){api('GET','/api/webdav').then(r=>{const t=document.getElementById('webdav-list');t.innerHTML='';window.webdavServers=r.data||[];(r.data||[]).forEach(x=>{t.innerHTML+='<tr><td>'+x.name+'</td><td>'+x.url+'</td><td><button class="btn small" onclick="testWebdav(\''+x.name+'\')">测试</button> <button class="btn small" onclick="editWebdav(\''+x.name+'\')">编辑</button> <button class="btn small danger" onclick="deleteWebdav(\''+x.name+'\')">删除</button></td></tr>'})})}
function loadStatus(){api('GET','/api/status').then(r=>{const t=document.getElementById('status-list');t.innerHTML='';(r.data||[]).forEach(x=>{t.innerHTML+='<tr><td>'+x.name+'</td><td>'+(x.last_run||'从未运行')+'</td><td>'+(x.next_run||'-')+'</td></tr>'})})}
function showTab(n){document.querySelectorAll('.tab').forEach(t=>t.classList.remove('active'));document.querySelector('.tab[onclick*="'+n+'"]').classList.add('active');document.querySelectorAll('[id$="-tab"]').forEach(t=>t.classList.add('hidden'));document.getElementById(n+'-tab').classList.remove('hidden');if(n==='status')loadStatus()}
function showTaskModal(d){document.getElementById('modal-title').textContent='添加任务';document.getElementById('task-form').reset();document.getElementById('task-original-name').value='';renderWebdavCheckboxes([]);document.getElementById('task-modal').classList.add('show');updateScheduleFields()}
function editTask(n){api('GET','/api/tasks/'+n).then(r=>{const d=r.data;document.getElementById('modal-title').textContent='编辑任务';document.getElementById('task-original-name').value=d.name;document.getElementById('task-name').value=d.name;document.getElementById('task-enabled').value=d.enabled?'true':'false';document.getElementById('task-paths').value=d.paths.map(p=>p.path).join('\n');document.getElementById('task-encrypt-pwd').value=d.encrypt_pwd||'';renderWebdavCheckboxes(d.webdav||[]);document.getElementById('task-schedule-type').value=d.schedule.type;document.getElementById('task-hour').value=d.schedule.hour||0;document.getElementById('task-minute').value=d.schedule.minute||0;document.getElementById('task-day').value=d.schedule.day||1;updateScheduleFields();document.getElementById('task-modal').classList.add('show')})}
function renderWebdavCheckboxes(selected){const c=document.getElementById('webdav-checkboxes');c.innerHTML='';if(!window.webdavServers||window.webdavServers.length===0){c.innerHTML='<span style="color:#999;font-size:0.75rem">未配置 WebDAV 服务器。请在 WebDAV 标签页添加。</span>';return}window.webdavServers.forEach(s=>{const chk=document.createElement('label');chk.style.cssText='display:flex;align-items:center;gap:0.25rem;padding:0.25rem;cursor:pointer;font-size:0.875rem';const cb=document.createElement('input');cb.type='checkbox';cb.value=s.name;cb.checked=(selected||[]).includes(s.name);chk.appendChild(cb);chk.appendChild(document.createTextNode(s.name));c.appendChild(chk)})}
function updateScheduleFields(){const t=document.getElementById('task-schedule-type').value;document.getElementById('day-group').style.display=t==='weekly'?'block':'none';document.getElementById('hour-group').style.display=t==='hourly'?'none':'block'}
function closeModal(n){document.getElementById(n).classList.remove('show')}
document.getElementById('task-form').onsubmit=function(e){e.preventDefault();const n=document.getElementById('task-name').value,on=document.getElementById('task-original-name').value,paths=document.getElementById('task-paths').value.split('\n').filter(p=>p.trim()).map(p=>({path:p.trim()}));const webdavSelected=Array.from(document.querySelectorAll('#webdav-checkboxes input:checked')).map(cb=>cb.value);const d={name:n,enabled:document.getElementById('task-enabled').value==='true',paths:paths,webdav:webdavSelected,schedule:{type:document.getElementById('task-schedule-type').value,hour:parseInt(document.getElementById('task-hour').value),minute:parseInt(document.getElementById('task-minute').value),day:parseInt(document.getElementById('task-day').value)},encrypt_pwd:document.getElementById('task-encrypt-pwd').value};(on?api('PUT','/api/tasks/'+on,d):api('POST','/api/tasks',d)).then(r=>{closeModal('task-modal');loadTasks()})};
document.getElementById('webdav-form').onsubmit=function(e){e.preventDefault();const on=document.getElementById('webdav-original-name').value,d={name:document.getElementById('webdav-name').value,url:document.getElementById('webdav-url').value,username:document.getElementById('webdav-username').value,password:document.getElementById('webdav-password').value,timeout:parseInt(document.getElementById('webdav-timeout').value)};(on?api('PUT','/api/webdav/'+on,d):api('POST','/api/webdav',d)).then(r=>{closeModal('webdav-modal');loadWebdav()})};
function runTask(n){api('POST','/api/tasks/run/'+n).then(r=>alert(r.message))}
function deleteTask(n){if(confirm('确定删除任务 '+n+'？'))api('DELETE','/api/tasks/'+n).then(loadTasks)}
function deleteWebdav(n){if(confirm('确定删除服务器 '+n+'？'))api('DELETE','/api/webdav/'+n).then(loadWebdav)}
function clearLogs(){document.getElementById('log-output').innerHTML=''}
function showWebdavModal(){document.getElementById('webdav-modal-title').textContent='添加 WebDAV 服务器';document.getElementById('webdav-form').reset();document.getElementById('webdav-original-name').value='';document.getElementById('webdav-modal').classList.add('show')}
function editWebdav(n){api('GET','/api/webdav/'+n).then(r=>{const d=r.data;document.getElementById('webdav-modal-title').textContent='编辑 WebDAV 服务器';document.getElementById('webdav-original-name').value=d.name;document.getElementById('webdav-name').value=d.name;document.getElementById('webdav-url').value=d.url;document.getElementById('webdav-username').value=d.username||'';document.getElementById('webdav-password').value='';document.getElementById('webdav-timeout').value=d.timeout||300;document.getElementById('webdav-modal').classList.add('show')})}
function testWebdav(n){api('POST','/api/webdav/test/'+n).then(r=>alert(r.success?'连接成功: '+r.message:'连接失败: '+r.message))}
connectWS();loadTasks();loadWebdav();
</script>
</body>
</html>`
