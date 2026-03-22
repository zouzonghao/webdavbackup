# WebDAV Backup Pro

一个综合性的备份和同步工具，支持本地文件备份和NodeImage图床同步两种功能，将所有数据流式上传到WebDAV服务器。

## 设计理念

本工具的设计原则是**专注、简单、职责单一**：

- **双模式备份**：既支持本地文件备份，也支持NodeImage图床同步
- **只管备份，不管存储**：备份文件上传到 WebDAV 后，存储空间管理、旧备份清理由用户自行处理
- **只管上传，不管恢复**：恢复操作通过手动下载备份文件解压即可，无需工具支持
- **实时日志推送**：通过WebSocket实时推送日志到Web界面，支持两种任务的实时监控
- **内存监控**：任务完成后输出峰值内存使用情况，便于监控资源消耗
- **明文配置**：配置文件中的密码明文存储，由用户自行保障配置文件安全
- **分离配置**：本地备份和NodeImage同步任务分开配置，逻辑清晰

## 功能边界

| 功能 | 是否支持 | 说明 |
|------|----------|------|
| 定时备份 | ✅ | 内置调度器 |
| 手动备份 | ✅ | WebUI 或命令行触发 |
| 多 WebDAV | ✅ | 支持同时上传到多个服务器 |
| 流式传输 | ✅ | 不占用本地磁盘和内存 |
| AES-256 加密 | ✅ | ZIP AES-256 加密，Windows 可直接解压 |
| Web 管理 | ✅ | 内嵌管理界面 |
| 实时日志 | ✅ | WebSocket 推送 |
| 密码回调 | ❌ | 编辑任务页面可查看密码 |
| 备份保留策略 | ❌ | 由用户在 WebDAV 端管理 |
| 备份历史记录 | ❌ | 通过系统日志查看 |
| 恢复功能 | ❌ | 手动下载解压 |
| 通知推送 | ❌ | 通过日志查看执行结果 |
| HTTPS | ❌ | 使用反向代理（nginx/caddy） |
| 单元测试 | ❌ | 个人工具，手动验证即可 |
| 配置验证 | ❌ | 配置错误会在运行时报错 |
| 健康检查端点 | ❌ | 通过日志监控进程状态 |
| HTTP 请求日志 | ❌ | 减少日志噪音 |
| 优雅关闭 | ✅ | 收到信号后停止 Scheduler 和 HTTP Server |
| 自定义备份路径 | ❌ | 使用绝对路径，解压时注意 |
| TLS 证书跳过 | ❌ | 请使用正规证书 |

## 架构

```
webdav-backup/
├── config/config.go              # 配置管理（YAML 格式，支持两种任务类型）
├── logger/logger.go              # 日志模块（输出到 stdout）
├── logger/ws_logger.go           # WebSocket 增强日志模块
├── backup/backup.go              # 本地备份模块（ZIP 流式打包）
├── backup/executor_legacy.go     # 向后兼容的执行器
├── engine/executor.go            # 统一执行引擎（支持两种任务类型）
├── nodeimage/client.go           # NodeImage API 客户端（增量/全量同步）
├── webdav/client.go              # WebDAV 客户端（向后兼容）
├── webdav/client_enhanced.go     # 增强版WebDAV客户端（完整功能）
├── websocket/hub.go              # WebSocket 连接管理
├── scheduler/scheduler.go        # 内置定时任务调度器
├── webserver/server.go           # Web 服务器 + 内嵌管理界面
├── public/                       # 前端静态文件
│   ├── index.html               # 综合性管理界面
│   ├── login.html               # 登录页面
│   ├── style.css                # Solarized 米色调主题样式
│   └── script.js                # 前端交互脚本
└── main.go                      # 主程序入口
```

### 核心组件

| 组件 | 功能 |
|------|------|
| Config | YAML 配置文件解析，支持热更新 |
| Logger | 结构化日志输出到 stdout |
| Backup | ZIP 流式打包，支持 AES-256 加密 |
| WebDAV Client | WebDAV 协议客户端，流式上传 |
| Scheduler | 内置定时任务调度，支持 hourly/daily/weekly |
| WebServer | 内嵌 Web 管理界面，Basic Auth 认证 |

### 备份流程

```
源文件/目录 → ZIP 打包压缩 → AES-256 加密（可选） → 流式上传到 WebDAV
```

**特点**：全程流式传输，不写入本地硬盘，不占用内存。

### ZIP AES-256 加密

如果任务配置了加密密码，备份文件会使用 ZIP AES-256 加密：

- **加密算法**：AES-256（WinZip AES 加密标准）
- **文件后缀**：`.zip`
- **兼容性**：Windows 可用 7-Zip、WinZip、Bandizip 等工具直接解压

**解压方法**：

Windows 用户可直接使用 7-Zip 等工具打开 ZIP 文件，输入密码即可解压。

Linux/macOS 用户可使用 7z 命令：

```bash
# 安装 p7zip（如未安装）
# Ubuntu/Debian: apt install p7zip-full
# Alpine: apk add p7zip
# macOS: brew install p7zip

# 解压到当前目录
7z x backup.zip -p"your-password"

# 解压到指定目录
7z x backup.zip -p"your-password" -o/
```

> ⚠️ **安全提示**：密码以明文存储在配置文件中，编辑任务页面可直接查看。请确保配置文件和 Web 界面的访问安全。

## 使用方式

### 编译

```bash
# 静态编译（推荐）
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o webdav-backup .

# 或本地编译
go build -o webdav-backup .
```

### 命令行参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| -config | 配置文件路径 | ./config.yaml |
| -task | 执行指定任务 | - |
| -list | 列出所有任务 | false |
| -run | 一次性运行模式（执行后退出） | false |
| -version | 显示版本 | false |

### 运行模式

```bash
# 守护进程模式（启动 Web 管理界面 + 定时任务）
./webdav-backup

# 一次性运行模式
./webdav-backup -run

# 运行指定任务
./webdav-backup -run -task mytask
```

### Systemd 服务示例

```ini
# /etc/systemd/system/webdav-backup.service
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
# 启用服务
systemctl enable webdav-backup
systemctl start webdav-backup

# 查看日志（备份执行记录）
journalctl -u webdav-backup -f
```

### 反向代理配置（HTTPS）

推荐使用 Nginx 或 Caddy 作为反向代理：

**Nginx 示例：**
```nginx
server {
    listen 443 ssl;
    server_name backup.example.com;
    
    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;
    
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

**Caddy 示例：**
```
backup.example.com {
    reverse_proxy localhost:8080
}
```

## 配置

配置文件默认为 `config.yaml`，首次运行会自动创建空配置。

```yaml
webdav:
  - name: myserver
    url: https://dav.example.com/backup
    username: user
    password: pass
    timeout: 300

tasks:
  - name: daily-backup
    enabled: true
    paths:
      - path: /home/user/documents
      - path: /etc/config.yaml
    webdav:
      - myserver
    schedule:
      type: daily
      hour: 2
      minute: 0
    encrypt_pwd: "your-secret-password"

webserver:
  enabled: true
  port: 8080
  username: admin
  password: admin
```

### 配置说明

#### WebDAV 服务器

| 字段 | 说明 |
|------|------|
| name | 服务器名称（任务引用用） |
| url | WebDAV 服务器地址 |
| username | 用户名 |
| password | 密码（明文存储） |
| timeout | 超时时间（秒） |

#### 备份任务

| 字段 | 说明 |
|------|------|
| name | 任务名称 |
| enabled | 是否启用 |
| paths | 备份路径列表（自动识别文件/目录） |
| webdav | WebDAV 服务器名称列表 |
| schedule | 调度配置 |
| encrypt_pwd | 加密密码（可选，用于 ZIP AES-256 加密） |

#### 调度配置

| 类型 | 说明 | 示例 |
|------|------|------|
| hourly | 每小时 | `{type: hourly, minute: 30}` |
| daily | 每天 | `{type: daily, hour: 2, minute: 0}` |
| weekly | 每周 | `{type: weekly, day: 1, hour: 2, minute: 0}` |

`day` 值：0=周日, 1=周一, ..., 6=周六

## Web 管理界面

启动后访问 `http://localhost:8080`，默认账号密码：`admin/admin`

功能：
- 任务管理（添加、编辑、删除、运行）
- WebDAV 服务器管理
- 实时日志查看
- 任务状态监控

## 备份文件格式

- 文件名：`任务名_YYYYMMDD_HHMMSS.zip`
- 压缩格式：ZIP（Deflate）
- 加密格式：AES-256（可选，WinZip 标准）

### 恢复备份

**未加密备份**：

```bash
# Linux/macOS
unzip backup.zip -d /

# 或查看压缩包内容
unzip -l backup.zip
```

**加密备份**：

Windows 用户直接用 7-Zip 打开，输入密码解压。

Linux/macOS 用户：

```bash
7z x backup.zip -p"your-password" -o/
```

**注意**：由于使用绝对路径，解压时会恢复到备份时的原始位置。

### 日志与执行记录

程序日志输出到 stdout，由系统服务管理器处理：

```bash
# systemd 环境下查看日志
journalctl -u webdav-backup -f

# Docker 环境下查看日志
docker logs -f container-name

# 查看特定时间段的备份记录
journalctl -u webdav-backup --since "2024-01-01" --until "2024-01-02"
```

## 许可证

MIT License
