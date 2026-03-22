# WebDAV Backup

一个综合性的备份与同步工具，整合了**本地文件备份**和 **NodeImage 图床同步**两种功能，通过 WebDAV 协议实现云端存储。

---

## 目录

- [项目起源](#项目起源)
- [系统架构](#系统架构)
- [核心模块详解](#核心模块详解)
- [数据流与执行流程](#数据流与执行流程)
- [配置说明](#配置说明)
- [部署与使用](#部署与使用)
- [API 参考](#api-参考)

---

## 项目起源

本项目由两个独立项目整合而来：

| 原项目 | 功能 | 整合后模块 |
|--------|------|-----------|
| `webdavbackup` | 本地文件定时备份到 WebDAV | 本地备份任务 (`local_tasks`) |
| `nodeimage_webdav` | NodeImage 图床同步到 WebDAV | NodeImage 同步任务 (`nodeimage_tasks`) |

整合后的系统共享以下基础设施：
- 统一的配置管理
- 统一的 WebDAV 客户端
- 统一的调度器
- 统一的 Web 管理界面
- 统一的日志系统

---

## 系统架构

### 整体架构图

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              main.go                                    │
│                         (程序入口、命令行解析)                            │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                    ┌───────────────┼───────────────┐
                    ▼               ▼               ▼
            ┌───────────┐   ┌───────────┐   ┌───────────┐
            │  Config   │   │  Logger   │   │  Engine   │
            │ (配置管理) │   │ (日志系统) │   │ (执行引擎) │
            └───────────┘   └───────────┘   └───────────┘
                    │               │               │
                    └───────────────┼───────────────┘
                                    ▼
                    ┌───────────────────────────────┐
                    │          Scheduler            │
                    │        (任务调度器)            │
                    └───────────────────────────────┘
                                    │
                    ┌───────────────┴───────────────┐
                    ▼                               ▼
        ┌─────────────────────┐         ┌─────────────────────┐
        │   LocalBackupTask   │         │  NodeImageSyncTask  │
        │     (本地备份)       │         │    (图床同步)        │
        └─────────────────────┘         └─────────────────────┘
                    │                               │
                    └───────────────┬───────────────┘
                                    ▼
                    ┌───────────────────────────────┐
                    │        WebDAV Client          │
                    │      (WebDAV 协议客户端)       │
                    └───────────────────────────────┘
                                    │
                                    ▼
                    ┌───────────────────────────────┐
                    │       WebDAV Server           │
                    │   (坚果云/Nextcloud/自建等)    │
                    └───────────────────────────────┘
```

### 目录结构

```
webdav-backup/
├── main.go                    # 程序入口，命令行解析，模式选择
├── config/
│   └── config.go              # 配置结构定义、加载、保存、热更新
├── engine/
│   └── executor.go            # 统一执行引擎，任务分发，流式处理
├── scheduler/
│   └── scheduler.go           # 定时任务调度器，支持 hourly/daily/weekly
├── webdav/
│   └── client.go              # WebDAV 协议客户端，流式上传
├── nodeimage/
│   └── client.go              # NodeImage API 客户端，增量/全量同步
├── logger/
│   └── logger.go              # 多格式日志系统，WebSocket 回调
├── webserver/
│   └── server.go              # HTTP 服务器，RESTful API，WebSocket
├── public/                    # 前端静态文件 (embed 嵌入)
│   ├── index.html             # 管理界面
│   ├── login.html             # 登录页面
│   ├── script.js              # 前端交互
│   └── style.css              # 样式
├── config_example.yaml        # 配置示例
├── Dockerfile                 # Docker 构建文件
├── docker-compose.yaml        # Docker Compose 配置
└── Makefile                   # 构建脚本
```

---

## 核心模块详解

### 1. 配置模块 (config/config.go)

**职责**：配置文件的加载、解析、保存和热更新。

**核心数据结构**：

```go
type Config struct {
    WebDAV         []WebDAVConfig      // WebDAV 服务器列表
    LocalTasks     []LocalBackupTask   // 本地备份任务
    NodeImageTasks []NodeImageSyncTask // NodeImage 同步任务
    WebServer      WebServerConfig     // Web 服务配置
    Log            LogConfig           // 日志配置
}

type ScheduleConfig struct {
    Type   string  // "hourly" | "daily" | "weekly"
    Day    int     // 周几 (0=周日, 1-6=周一至周六)
    Hour   int     // 小时 (0-23)
    Minute int     // 分钟 (0-59)
}
```

**实现特点**：
- 使用 `gopkg.in/yaml.v3` 解析 YAML
- 支持配置文件不存在时自动创建默认配置
- 提供任务查询方法：`GetTaskByName()`, `GetWebDAVByName()`
- 支持运行时动态更新配置并持久化

---

### 2. 执行引擎 (engine/executor.go)

**职责**：统一任务执行入口，处理本地备份和 NodeImage 同步两种任务类型。

#### 2.1 本地备份任务 (ExecuteLocalTask)

**执行流程**：

```
┌──────────────┐    ┌──────────────┐    ┌──────────────┐    ┌──────────────┐
│  路径验证    │ -> │  文件遍历    │ -> │  ZIP 流式打包 │ -> │  流式上传    │
└──────────────┘    └──────────────┘    └──────────────┘    └──────────────┘
```

**关键技术点**：

1. **流式 ZIP 打包**：
   ```go
   func (e *Executor) createStream(task *config.LocalBackupTask) (*StreamResult, error) {
       pr, pw := io.Pipe()  // 创建管道
       
       go func() {
           zipWriter := zip.NewWriter(pw)
           // 遍历文件并写入 ZIP
           for _, f := range files {
               e.addFileToZip(zipWriter, f.path, f.info, ...)
           }
           zipWriter.Close()
           pw.Close()
       }()
       
       return &StreamResult{Stream: pr, ...}
   }
   ```
   - 使用 `io.Pipe` 实现零内存占用
   - ZIP 压缩与上传并行进行

2. **AES-256 加密**：
   ```go
   if password != "" {
       writer, err = zipWriter.Encrypt(header.Name, password, zip.AES256Encryption)
   }
   ```
   - 使用 `github.com/mzky/zip` 库支持 WinZip AES-256 标准
   - 兼容 Windows 7-Zip 直接解压

3. **内存监控**：
   ```go
   type memTracker struct {
       maxAlloc uint64
       maxSys   uint64
   }
   ```
   - 任务完成后输出峰值内存使用情况

#### 2.2 NodeImage 同步任务 (ExecuteNodeImageTask)

**两种同步模式**：

| 模式 | 认证方式 | 特点 | 适用场景 |
|------|----------|------|----------|
| `incremental` | API Key | 只获取最近图片，不同步删除 | 日常增量备份 |
| `full` | Cookie | 获取全部图片，删除多余文件 | 完整镜像同步 |

**执行流程**：

```
┌──────────────┐    ┌──────────────┐    ┌──────────────┐    ┌──────────────┐
│  获取图片列表 │ -> │  对比现有文件 │ -> │  并发上传/删除 │ -> │  更新缓存    │
└──────────────┘    └──────────────┘    └──────────────┘    └──────────────┘
```

**关键技术点**：

1. **文件缓存机制**：
   ```go
   var webdavCache *cacheEntry
   
   func getCache() []webdav.FileInfo { ... }
   func setCache(files []webdav.FileInfo) { ... }
   ```
   - 缓存 WebDAV 文件列表，避免重复请求
   - 最大缓存 10000 个文件

2. **并发控制**：
   ```go
   semaphore := make(chan struct{}, concurrency)
   
   for _, img := range filesToUpload {
       go func(img nodeimage.ImageInfo) {
           semaphore <- struct{}{}
           defer func() { <-semaphore }()
           // 上传逻辑
       }(img)
   }
   ```
   - 使用信号量控制并发数
   - 默认并发数为 5

3. **重试机制**：
   ```go
   const maxRetries = 3
   const retryDelay = 2 * time.Second
   
   for attempt := 1; attempt <= maxRetries; attempt++ {
       // 尝试上传
       if err == nil { return nil }
       time.Sleep(retryDelay)
   }
   ```

---

### 3. 调度器 (scheduler/scheduler.go)

**职责**：管理定时任务的调度、执行和状态跟踪。

**核心结构**：

```go
type Scheduler struct {
    tasks          map[string]*scheduledTask  // 已调度任务
    taskFunc       TaskFunc                   // 任务执行函数
    executionStore map[string]*ExecutionStatus // 执行状态存储
    runningTasks   map[string]bool            // 正在运行的任务
}

type scheduledTask struct {
    task     interface{}  // LocalBackupTask 或 NodeImageSyncTask
    taskName string
    taskType string       // "local" 或 "nodeimage"
    stopChan chan struct{}
    lastRun  time.Time
    nextRun  time.Time
}
```

**调度算法**：

```go
func calculateNextRun(schedule *config.ScheduleConfig) time.Time {
    now := time.Now()
    
    switch schedule.Type {
    case "hourly":
        // 每小时在指定分钟执行
    case "daily":
        // 每天在指定时间执行
    case "weekly":
        // 每周在指定星期几的指定时间执行
    }
}
```

**防重复执行**：

```go
func (s *Scheduler) tryStartTask(name string) bool {
    s.runningMu.Lock()
    defer s.runningMu.Unlock()
    if s.runningTasks[name] {
        return false  // 任务已在运行
    }
    s.runningTasks[name] = true
    return true
}
```

---

### 4. WebDAV 客户端 (webdav/client.go)

**职责**：实现 WebDAV 协议的文件操作。

**核心方法**：

| 方法 | HTTP 方法 | 功能 |
|------|-----------|------|
| `UploadStream` | PUT | 流式上传文件 |
| `ListFiles` | PROPFIND | 列出目录文件 |
| `Mkdir` | MKCOL | 创建目录 |
| `Delete` | DELETE | 删除文件 |
| `TestConnection` | PROPFIND | 测试连接 |
| `EnsureDirectory` | - | 确保目录存在 |

**流式上传实现**：

```go
func (c *EnhancedClient) UploadStream(reader io.Reader, size int64, remotePath string) error {
    req, err := c.newRequest(context.Background(), "PUT", remotePath, reader)
    if err != nil {
        return fmt.Errorf("创建上传请求失败: %w", err)
    }
    
    resp, err := c.do(req)
    // ...
}
```

**PROPFIND 解析**：

```go
type propfindResponse struct {
    XMLName   xml.Name   `xml:"multistatus"`
    Responses []response `xml:"response"`
}

func (c *EnhancedClient) ListFiles(path string) ([]FileInfo, error) {
    // 发送 PROPFIND 请求
    // 解析 XML 响应
    // 返回文件列表
}
```

---

### 5. NodeImage 客户端 (nodeimage/client.go)

**职责**：与 NodeImage API 交互，获取图片列表和下载图片。

**两种认证方式**：

```go
type Client struct {
    httpClient *http.Client
    cookie     string  // 全量同步认证
    apiKey     string  // 增量同步认证
    baseURL    string
}
```

**API 端点**：

| 模式 | 端点 | 认证 | 返回数据 |
|------|------|------|----------|
| 全量 | `/api/images` | Cookie | 全部图片（分页） |
| 增量 | `/api/v1/list` | X-API-Key | 最近图片 |

**压缩支持**：

```go
func getDecompressionReader(resp *http.Response) (io.Reader, error) {
    switch resp.Header.Get("Content-Encoding") {
    case "zstd":
        return zstd.NewReader(resp.Body)
    case "gzip":
        return gzip.NewReader(resp.Body)
    default:
        return resp.Body, nil
    }
}
```

---

### 6. 日志模块 (logger/logger.go)

**职责**：提供多格式日志输出和 WebSocket 回调支持。

**四种日志格式**：

| 格式 | 特点 | 适用场景 |
|------|------|----------|
| `simple` | Emoji + 颜色，现代简洁 | 日常使用 |
| `detailed` | 包含文件名、行号 | 调试排错 |
| `json` | JSON 结构化 | 日志收集 |
| `classic` | 传统格式带颜色 | 兼容旧系统 |

**WebSocket 回调**：

```go
func SetLogCallback(cb func(level, msg string)) {
    defaultLogger.callback = func(l Level, m string) {
        cb(l.String(), m)
    }
}
```

日志输出时自动触发回调，实现实时日志推送到 Web 界面。

---

### 7. Web 服务器 (webserver/server.go)

**职责**：提供 RESTful API 和 WebSocket 服务。

**认证机制**：

```go
type Claims struct {
    Username string `json:"username"`
    jwt.RegisteredClaims
}

func (s *Server) generateToken() (string, error) {
    claims := &Claims{
        Username: s.config.WebServer.Username,
        RegisteredClaims: jwt.RegisteredClaims{
            ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
        },
    }
    token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
    return token.SignedString(jwtSecret)
}
```

**API 路由**：

| 路由 | 方法 | 功能 |
|------|------|------|
| `/api/auth` | GET | 验证认证状态 |
| `/api/tasks` | GET/POST | 任务列表/创建 |
| `/api/tasks/{name}` | GET/PUT/DELETE | 任务详情/更新/删除 |
| `/api/tasks/{name}/run` | POST | 执行任务 |
| `/api/tasks/run` | POST | 执行所有任务 |
| `/api/webdav` | GET/POST | WebDAV 列表/添加 |
| `/api/webdav/{name}/test` | POST | 测试连接 |
| `/api/config` | GET | 获取配置 |
| `/api/status` | GET | 获取状态 |
| `/ws` | WebSocket | 实时日志 |

**WebSocket 实现**：

```go
func (s *Server) handleWebSocket(ws *websocket.Conn) {
    // 注册客户端
    s.wsClients[ws] = true
    
    // 发送历史日志
    for _, entry := range s.logBuffer {
        websocket.JSON.Send(ws, entry)
    }
    
    // 保持连接，接收心跳
    for {
        websocket.Message.Receive(ws, &msg)
    }
}
```

---

## 数据流与执行流程

### 本地备份数据流

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           本地备份数据流                                 │
└─────────────────────────────────────────────────────────────────────────┘

  本地文件系统                    内存处理                      远程存储
  ─────────────                  ────────                      ────────
       │                            │                             │
       │  ┌─────────────┐           │                             │
       ├──│ filepath.Walk│──────────┼──▶ 收集文件列表              │
       │  └─────────────┘           │                             │
       │                            │                             │
       │  ┌─────────────┐           │                             │
       ├──│ os.Open    │───────────┼──▶ 读取文件内容              │
       │  └─────────────┘           │                             │
       │                            │                             │
       │                            │  ┌──────────────────┐       │
       │                            ├──│ io.Pipe (Writer) │───────┼──▶ ZIP 数据流
       │                            │  └──────────────────┘       │
       │                            │                             │
       │                            │  ┌──────────────────┐       │
       │                            ├──│ zip.Encrypt      │───────┼──▶ AES-256 加密
       │                            │  └──────────────────┘       │
       │                            │                             │
       │                            │  ┌──────────────────┐       │
       │                            └──│ io.Pipe (Reader) │───────┼──▶ HTTP PUT 请求体
       │                               └──────────────────┘       │
       │                                                          │
       │                                                          ▼
       │                                              ┌──────────────────┐
       │                                              │   WebDAV Server  │
       │                                              │  (备份文件存储)   │
       │                                              └──────────────────┘
```

**关键特点**：
- 全程流式传输，不落盘
- 压缩、加密、上传并行进行
- 内存占用恒定，不随文件大小增长

### NodeImage 同步数据流

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         NodeImage 同步数据流                             │
└─────────────────────────────────────────────────────────────────────────┘

  NodeImage API                  本地处理                      WebDAV Server
  ─────────────                  ────────                      ────────────
       │                            │                             │
       │  GET /api/images           │                             │
       │  (Cookie/API Key)          │                             │
       │ ─────────────────────────▶ │                             │
       │                            │                             │
       │  图片列表 (JSON)           │                             │
       │ ◀───────────────────────── │                             │
       │                            │                             │
       │                            │  对比本地缓存                 │
       │                            │  ┌──────────────────┐       │
       │                            │  │ 计算差异:        │       │
       │                            │  │ - 待上传列表     │       │
       │                            │  │ - 待删除列表     │       │
       │                            │  └──────────────────┘       │
       │                            │                             │
       │  GET image.jpg             │                             │
       │ ─────────────────────────▶ │                             │
       │                            │                             │
       │  图片二进制流              │                             │
       │ ◀───────────────────────── │                             │
       │                            │                             │
       │                            │  HTTP PUT (流式上传)        │
       │                            │ ─────────────────────────▶ │
       │                            │                             │
       │                            │  HTTP DELETE (删除多余)     │
       │                            │ ─────────────────────────▶ │
       │                            │                             │
       │                            │  更新缓存                    │
       │                            │ ◀────────────────────────── │
```

---

## 配置说明

### 完整配置示例

```yaml
# WebDAV 服务器配置
webdav:
  - name: jianguoyun
    url: https://dav.jianguoyun.com/dav
    username: your_email@example.com
    password: your_app_password
    timeout: 300

# 本地备份任务
local_tasks:
  - name: daily-documents
    type: local
    enabled: true
    paths:
      - path: /home/user/documents
      - path: /home/user/photos
    webdav:
      - jianguoyun
    schedule:
      type: daily
      hour: 2
      minute: 0
    encrypt_pwd: "your-encryption-password"
    base_path: "backups"

# NodeImage 同步任务
nodeimage_tasks:
  - name: nodeimage-incremental
    type: nodeimage
    enabled: true
    sync_mode: incremental
    nodeimage:
      api_key: "your-api-key"
      cookie: ""
      api_url: "https://api.nodeimage.com/api/images"
      base_path: "/nodeimage"
    webdav:
      - jianguoyun
    schedule:
      type: daily
      hour: 4
      minute: 0
    concurrency: 5

# Web 管理界面
webserver:
  host: 0.0.0.0
  port: 8080
  username: admin
  password: admin123

# 日志配置
log:
  format: simple
  level: debug
  no_color: false
```

### 配置字段说明

#### WebDAV 配置

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | ✓ | 服务器名称，任务中引用 |
| `url` | string | ✓ | WebDAV 服务器地址 |
| `username` | string | ✓ | 用户名 |
| `password` | string | ✓ | 密码（明文存储） |
| `timeout` | int | | 超时时间（秒），默认 300 |

#### 本地备份任务

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | ✓ | 任务名称（唯一） |
| `type` | string | ✓ | 固定为 `local` |
| `enabled` | bool | | 是否启用，默认 false |
| `paths` | []BackupItem | ✓ | 备份路径列表 |
| `webdav` | []string | ✓ | 目标 WebDAV 名称列表 |
| `schedule` | ScheduleConfig | ✓ | 调度配置 |
| `encrypt_pwd` | string | | ZIP 加密密码（空则不加密） |
| `base_path` | string | | WebDAV 存储路径 |

#### NodeImage 同步任务

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | ✓ | 任务名称（唯一） |
| `type` | string | ✓ | 固定为 `nodeimage` |
| `enabled` | bool | | 是否启用，默认 false |
| `sync_mode` | string | ✓ | `incremental` 或 `full` |
| `nodeimage.api_key` | string | | API Key（增量同步必需） |
| `nodeimage.cookie` | string | | Cookie（全量同步必需） |
| `nodeimage.base_path` | string | | WebDAV 存储路径 |
| `webdav` | []string | ✓ | 目标 WebDAV 名称列表 |
| `schedule` | ScheduleConfig | ✓ | 调度配置 |
| `concurrency` | int | | 并发数，默认 5 |

#### 调度配置

| 类型 | 必填字段 | 示例 |
|------|----------|------|
| `hourly` | `minute` | `{type: hourly, minute: 30}` |
| `daily` | `hour`, `minute` | `{type: daily, hour: 2, minute: 0}` |
| `weekly` | `day`, `hour`, `minute` | `{type: weekly, day: 0, hour: 2, minute: 0}` |

`day` 值：0=周日, 1=周一, ..., 6=周六

---

## 部署与使用

### 编译

```bash
# 静态编译（推荐）
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o webdav-backup .

# 本地编译
go build -o webdav-backup .
```

### 命令行参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-config` | 配置文件路径 | `./config.yaml` |
| `-task` | 执行指定任务 | - |
| `-list` | 列出所有任务 | `false` |
| `-run` | 一次性运行模式 | `false` |
| `-version` | 显示版本 | `false` |
| `-log-format` | 日志格式 | 配置文件值 |
| `-log-level` | 日志级别 | 配置文件值 |
| `-log-styles` | 显示日志样式示例 | `false` |

### 运行模式

```bash
# 守护进程模式（Web 界面 + 定时任务）
./webdav-backup

# 一次性运行所有启用任务
./webdav-backup -run

# 运行指定任务
./webdav-backup -run -task daily-documents

# 列出所有任务
./webdav-backup -list
```

### Docker 部署

```bash
# 构建镜像
docker build -t webdav-backup .

# 运行容器
docker run -d \
  --name webdav-backup \
  -v /path/to/config.yaml:/app/config.yaml \
  -v /path/to/backup:/backup:ro \
  -p 8080:8080 \
  webdav-backup
```

### Docker Compose

```yaml
version: '3'
services:
  webdav-backup:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./config.yaml:/app/config.yaml
      - /home/user/documents:/backup/documents:ro
    restart: unless-stopped
```

### Systemd 服务

```ini
[Unit]
Description=WebDAV Backup Service
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/webdav-backup -config /etc/webdav-backup/config.yaml
Restart=always
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

```bash
systemctl enable webdav-backup
systemctl start webdav-backup
journalctl -u webdav-backup -f
```

### 反向代理（HTTPS）

**Nginx**：
```nginx
server {
    listen 443 ssl;
    server_name backup.example.com;
    
    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;
    
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

**Caddy**：
```
backup.example.com {
    reverse_proxy localhost:8080
}
```

---

## API 参考

### 认证

所有 API（除登录外）需要 JWT 认证，Token 通过 Cookie `auth` 传递。

**登录**：
```http
POST /login
Content-Type: application/json

{"username": "admin", "password": "admin123"}
```

### 任务管理

**获取任务列表**：
```http
GET /api/tasks
```

**创建任务**：
```http
POST /api/tasks
Content-Type: application/json

{
  "name": "new-task",
  "type": "local",
  "enabled": true,
  "paths": [{"path": "/home/user/data"}],
  "webdav": ["jianguoyun"],
  "schedule": {"type": "daily", "hour": 2, "minute": 0}
}
```

**执行任务**：
```http
POST /api/tasks/daily-documents/run
```

**执行所有任务**：
```http
POST /api/tasks/run
```

### WebDAV 管理

**测试连接**：
```http
POST /api/webdav/jianguoyun/test
```

### WebSocket

连接 `/ws` 接收实时日志：

```javascript
const ws = new WebSocket('ws://localhost:8080/ws');
ws.onmessage = (event) => {
  const log = JSON.parse(event.data);
  console.log(`[${log.level}] ${log.message}`);
};
```

---

## 备份文件格式

### 文件命名

```
{任务名}_{YYYYMMDD}_{HHMMSS}.zip
```

示例：`daily-documents_20240115_020000.zip`

### 解压方法

**未加密**：
```bash
unzip backup.zip -d /
```

**加密（AES-256）**：
```bash
# Linux/macOS (需要 p7zip)
7z x backup.zip -p"your-password" -o/

# Windows
# 使用 7-Zip、WinZip、Bandizip 等工具直接打开
```

---

## 许可证

MIT License
