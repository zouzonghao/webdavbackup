# WebDAV Backup

一个轻量级的备份工具，支持将指定目录/文件打包加密后上传到 WebDAV 服务器。

## 设计目标

- **轻量简洁**：单一可执行文件，无外部依赖，适合在 Alpine Linux 等精简系统上运行
- **安全可靠**：使用 AES-256-GCM 加密备份数据，保护敏感信息
- **灵活配置**：支持多个备份任务，每个任务可配置不同的备份路径、WebDAV 服务器和定时策略
- **Web 管理**：内置 Web 管理界面，方便配置和监控
- **内置调度**：不依赖系统 cron，程序内部自动调度任务

## 架构

```
webdav-backup/
├── config/config.go      # 配置管理（YAML 格式）
├── logger/logger.go      # 日志模块（输出到 stdout）
├── backup/backup.go      # 备份模块（打包 + 加密）
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
| Backup | Tar 打包 + Gzip 压缩 + AES-256-GCM 加密 |
| WebDAV Client | WebDAV 协议客户端，支持上传、下载、列表 |
| Scheduler | 内置定时任务调度，支持 hourly/daily/weekly |
| WebServer | 内嵌 Web 管理界面，Basic Auth 认证 |

### 备份流程

```
源文件/目录 → Tar 打包 → Gzip 压缩 → AES-256-GCM 加密 → 上传到 WebDAV
```

## 使用方式

### 编译

```bash
# 普通编译
go build -o webdav-backup .

# 静态编译（使用 musl）
make build-static
```

### 配置文件

首次运行会自动在工作目录创建 `config.yaml`：

```yaml
webdav:
  - name: jianguoyun
    url: https://dav.jianguoyun.com/dav/backup
    username: your-email@example.com
    password: your-password
    timeout: 300

encryption:
  password: your-strong-encryption-password

webserver:
  enabled: true
  port: 8080
  username: admin
  password: admin123

tasks:
  - name: app-config
    enabled: true
    paths:
      - path: /opt/app
        type: dir
      - path: /etc/app/config.yaml
        type: file
    webdav:
      - jianguoyun
    schedule:
      type: daily
      hour: 0
      minute: 0
    keep_history: 7

temp_dir: /tmp/webdav-backup
```

### 配置说明

#### WebDAV 配置

| 字段 | 说明 |
|------|------|
| name | WebDAV 服务器名称，任务中引用 |
| url | WebDAV 服务器地址 |
| username | 用户名 |
| password | 密码 |
| timeout | 超时时间（秒） |

#### 任务配置

| 字段 | 说明 |
|------|------|
| name | 任务名称 |
| enabled | 是否启用 |
| paths | 备份路径列表，type 可选 `dir` 或 `file` |
| webdav | WebDAV 服务器名称列表 |
| schedule | 定时策略 |
| keep_history | 保留历史版本数 |

#### 定时策略

| 类型 | 说明 | 配置字段 |
|------|------|----------|
| hourly | 每小时执行 | minute |
| daily | 每天执行 | hour, minute |
| weekly | 每周执行 | day (0=周日), hour, minute |

### 运行方式

#### 守护进程模式（推荐）

```bash
# 启动守护进程（Web 管理界面 + 定时任务）
./webdav-backup -daemon

# 使用 systemd 管理
./webdav-backup -config /etc/webdav-backup/config.yaml -daemon
```

#### 命令行模式

```bash
# 列出所有任务
./webdav-backup -list

# 执行单个任务
./webdav-backup -task app-config

# 执行所有启用的任务
./webdav-backup
```

### Web 管理界面

启动守护进程后，访问 `http://localhost:8080` 进入管理界面。

功能：
- **Tasks**：管理备份任务，支持创建、编辑、删除、手动执行
- **WebDAV**：管理 WebDAV 服务器，支持测试连接
- **Status**：查看任务执行状态

### 命令行参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| -config | 配置文件路径 | ./config.yaml |
| -task | 执行指定任务 | - |
| -list | 列出所有任务 | false |
| -daemon | 守护进程模式 | false |
| -version | 显示版本 | false |

### Systemd 服务示例

```ini
# /etc/systemd/system/webdav-backup.service
[Unit]
Description=WebDAV Backup Service
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/webdav-backup -config /etc/webdav-backup/config.yaml -daemon
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

## 备份文件格式

- 文件名：`任务名_YYYYMMDD_HHMMSS.tar.gz.enc`
- 加密算法：AES-256-GCM
- 压缩格式：Gzip
- 打包格式：Tar（保留原始目录结构）

### 恢复备份

```bash
# 解密
openssl enc -d -aes-256-gcm -in backup.tar.gz.enc -out backup.tar.gz -pass pass:your-password

# 解压
tar -xzf backup.tar.gz -C /
```

## 许可证

MIT License
