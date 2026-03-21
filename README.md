# WebDAV Backup

一个轻量级的备份工具，支持将指定目录/文件打包后流式上传到 WebDAV 服务器。

## 设计理念

本工具的设计原则是**专注、简单、职责单一**：

- **只管备份，不管存储**：备份文件上传到 WebDAV 后，存储空间管理、旧备份清理由用户自行处理
- **只管上传，不管恢复**：恢复操作通过手动下载备份文件解压即可，无需工具支持
- **日志即通知**：所有执行结果输出到 stdout，通过 systemd/docker 等服务管理器持久化，配合 WebUI 实时查看
- **明文配置**：配置文件中的密码明文存储，由用户自行保障配置文件安全

## 功能边界

| 功能 | 是否支持 | 说明 |
|------|----------|------|
| 定时备份 | ✅ | 内置调度器 |
| 手动备份 | ✅ | WebUI 或命令行触发 |
| 多 WebDAV | ✅ | 支持同时上传到多个服务器 |
| 流式传输 | ✅ | 不占用本地磁盘和内存 |
| Web 管理 | ✅ | 内嵌管理界面 |
| 实时日志 | ✅ | WebSocket 推送 |
| 备份保留策略 | ❌ | 由用户在 WebDAV 端管理 |
| 备份历史记录 | ❌ | 通过系统日志查看 |
| 恢复功能 | ❌ | 手动下载解压 |
| 通知推送 | ❌ | 通过日志查看执行结果 |
| HTTPS | ❌ | 使用反向代理（nginx/caddy） |
| 单元测试 | ❌ | 个人工具，手动验证即可 |
| 配置验证 | ❌ | 配置错误会在运行时报错 |
| 健康检查端点 | ❌ | 通过日志监控进程状态 |
| HTTP 请求日志 | ❌ | 减少日志噪音 |
| 优雅关闭 | ❌ | 下次运行继续备份即可 |
| 自定义备份路径 | ❌ | 使用绝对路径，解压时注意 |
| TLS 证书跳过 | ❌ | 请使用正规证书 |

## 架构

```
webdav-backup/
├── config/config.go       # 配置管理（YAML 格式）
├── logger/logger.go       # 日志模块（输出到 stdout）
├── backup/backup.go       # 备份模块（tar + gzip 流式打包）
├── webdav/client.go       # WebDAV 客户端
├── scheduler/scheduler.go # 内置定时任务调度器
├── webserver/server.go    # Web 服务器 + 内嵌管理界面
└── main.go                # 主程序入口
```

### 核心组件

| 组件 | 功能 |
|------|------|
| Config | YAML 配置文件解析，支持热更新 |
| Logger | 结构化日志输出到 stdout |
| Backup | Tar + Gzip 流式打包，直接输出到 WebDAV |
| WebDAV Client | WebDAV 协议客户端，流式上传 |
| Scheduler | 内置定时任务调度，支持 hourly/daily/weekly |
| WebServer | 内嵌 Web 管理界面，Basic Auth 认证 |

### 备份流程

```
源文件/目录 → Tar 打包 → Gzip 压缩 → 流式上传到 WebDAV
```

**特点**：全程流式传输，不写入本地硬盘，不占用内存。

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

- 文件名：`任务名_YYYYMMDD_HHMMSS.tar.gz`
- 压缩格式：Gzip
- 打包格式：Tar（保留原始目录结构）

### 恢复备份

备份文件使用绝对路径打包，解压时会恢复到原始位置：

```bash
# 恢复到原始路径（需要 root 权限）
tar -xzf backup.tar.gz -C /

# 或查看压缩包内容后再决定
tar -tzf backup.tar.gz
```

**注意**：由于使用绝对路径，解压时 `-C /` 会将文件恢复到备份时的原始位置。

`tar: Removing leading '/' from member names` 是正常提示，表示 tar 移除了路径前导斜杠（安全机制），配合 `-C /` 仍能恢复到原位。

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
