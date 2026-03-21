# WebDAV Backup

一个轻量级的备份工具，支持将指定目录/文件打包后流式上传到 WebDAV 服务器。

## 设计目标

- **轻量简洁**：单一可执行文件，无外部依赖，适合在 Alpine Linux 等精简系统上运行
- **零内存占用**：流式传输，不占用内存和硬盘
- **Web 管理**：内置 Web 管理界面，方便配置和监控
- **内置调度**：不依赖系统 cron，程序内部自动调度任务

## 架构

```
webdav-backup/
├── config/config.go      # 配置管理（YAML 格式）
├── logger/logger.go      # 日志模块（输出到 stdout）
├── backup/backup.go      # 备份模块（tar + gzip 流式打包）
├── webdav/client.go      # WebDAV 客户端
├── scheduler/scheduler.go # 内置定时任务调度器
├── webserver/server.go   # Web 服务器 + 内嵌管理界面
└── main.go               # 主程序入口
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

# 查看日志
journalctl -u webdav-backup -f
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
        type: dir
      - path: /etc/config.yaml
        type: file
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
| password | 密码 |
| timeout | 超时时间（秒） |

#### 备份任务

| 字段 | 说明 |
|------|------|
| name | 任务名称 |
| enabled | 是否启用 |
| paths | 备份路径列表 |
| paths[].path | 路径 |
| paths[].type | 类型：dir 或 file |
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

```bash
# 下载备份文件后解压
tar -xzf backup.tar.gz -C /restore/path
```

### 日志管理

程序日志输出到 stdout，由系统服务管理器处理：

```bash
# systemd 环境下查看日志
journalctl -u webdav-backup -f

# Docker 环境下查看日志
docker logs -f container-name
```

## 许可证

MIT License
