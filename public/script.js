let ws;
let webdavServers = [];
let logs = []; // 日志数组（按插入顺序）
const MAX_LOGS = 500; // 最大日志数量
let historyBatch = []; // 临时存储历史日志批次

// 日志过滤设置
let logFilters = {
    INFO: true,
    DEBUG: false
};

document.addEventListener('DOMContentLoaded', async () => {
    initTabs();
    initModals();
    initForms();
    connectWebSocket();
    await loadTasks();
    await loadWebDAV();
});

// Toast 通知系统
function showToast(type, title, message, duration = 3000) {
    const container = document.getElementById('toast-container');
    const toast = document.createElement('div');
    toast.className = `toast toast-${type}`;
    
    const icons = {
        success: '✓',
        error: '✕',
        info: 'ℹ',
        warning: '⚠'
    };
    
    toast.innerHTML = `
        <span class="toast-icon">${icons[type]}</span>
        <div class="toast-content">
            ${title ? `<div class="toast-title">${title}</div>` : ''}
            <div class="toast-message">${message}</div>
        </div>
        <button class="toast-close">&times;</button>
    `;
    
    container.appendChild(toast);
    
    // 关闭按钮事件
    const closeBtn = toast.querySelector('.toast-close');
    closeBtn.addEventListener('click', () => {
        removeToast(toast);
    });
    
    // 自动移除
    if (duration > 0) {
        setTimeout(() => {
            removeToast(toast);
        }, duration);
    }
    
    return toast;
}

function removeToast(toast) {
    if (toast.classList.contains('hiding')) return;
    
    toast.classList.add('hiding');
    setTimeout(() => {
        if (toast.parentNode) {
            toast.parentNode.removeChild(toast);
        }
    }, 300);
}

// 自定义确认弹窗
function showConfirm(title, message) {
    return new Promise((resolve) => {
        const modal = document.getElementById('confirm-modal');
        const titleEl = modal.querySelector('.confirm-title');
        const messageEl = modal.querySelector('.confirm-message');
        const cancelBtn = document.getElementById('confirm-cancel');
        const okBtn = document.getElementById('confirm-ok');
        
        titleEl.textContent = title;
        messageEl.textContent = message;
        modal.classList.add('show');
        
        const handleConfirm = () => {
            modal.classList.remove('show');
            resolve(true);
            cleanup();
        };
        
        const handleCancel = () => {
            modal.classList.remove('show');
            resolve(false);
            cleanup();
        };
        
        const cleanup = () => {
            okBtn.removeEventListener('click', handleConfirm);
            cancelBtn.removeEventListener('click', handleCancel);
        };
        
        okBtn.addEventListener('click', handleConfirm);
        cancelBtn.addEventListener('click', handleCancel);
    });
}

function initTabs() {
    const tabBtns = document.querySelectorAll('.tab-btn');
    tabBtns.forEach(btn => {
        btn.addEventListener('click', () => {
            tabBtns.forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            
            const tabId = btn.dataset.tab;
            document.querySelectorAll('main > section').forEach(s => s.classList.add('hidden'));
            document.getElementById(`${tabId}-tab`).classList.remove('hidden');
            
            if (tabId === 'status') {
                loadStatus();
            }
        });
    });
}

function initModals() {
    // 点击关闭按钮关闭弹窗
    document.querySelectorAll('.close-btn, .close-modal').forEach(btn => {
        btn.addEventListener('click', () => {
            document.querySelectorAll('.modal-overlay').forEach(m => m.classList.remove('show'));
        });
    });

    // 移除点击遮罩层关闭弹窗的功能，防止误触

    document.getElementById('add-task-btn').addEventListener('click', () => showTaskModal());
    document.getElementById('add-webdav-btn').addEventListener('click', () => showWebdavModal());
    document.getElementById('clear-log-btn').addEventListener('click', clearLogs);

    // 日志过滤按钮
    document.getElementById('log-filter-info').addEventListener('click', () => toggleLogFilter('INFO'));
    document.getElementById('log-filter-debug').addEventListener('click', () => toggleLogFilter('DEBUG'));

    document.getElementById('task-type').addEventListener('change', updateTaskFormFields);
    document.getElementById('task-schedule-type').addEventListener('change', updateScheduleFields);
    document.getElementById('task-sync-mode').addEventListener('change', updateSyncModeHints);
}

function initForms() {
    document.getElementById('task-form').addEventListener('submit', handleTaskSubmit);
    document.getElementById('webdav-form').addEventListener('submit', handleWebdavSubmit);
}

function connectWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${protocol}//${window.location.host}/ws`);

    ws.onopen = () => {
        const now = new Date();
        const timeStr = now.toLocaleTimeString('zh-CN', { hour12: false });
        addRealtimeLog('WebSocket 连接成功', 'INFO', timeStr);
    };

    ws.onmessage = (event) => {
        const data = JSON.parse(event.data);
        handleWebSocketMessage(data);
    };

    ws.onclose = () => {
        const now = new Date();
        const timeStr = now.toLocaleTimeString('zh-CN', { hour12: false });
        addRealtimeLog('WebSocket 连接断开，3秒后重连...', 'WARN', timeStr);
        setTimeout(connectWebSocket, 3000);
    };

    ws.onerror = (error) => {
        console.error('WebSocket error:', error);
        const now = new Date();
        const timeStr = now.toLocaleTimeString('zh-CN', { hour12: false });
        addRealtimeLog('WebSocket 连接错误', 'ERROR', timeStr);
    };
}

// 添加历史日志（替换整个数组）
function addHistoryLogs(historyLogs) {
    logs = historyLogs.slice(-MAX_LOGS); // 限制数量
    renderLogs();
    scrollToBottom();
}

// 添加实时日志（追加到末尾）
function addRealtimeLog(message, level, time) {
    logs.push({
        time: time,
        level: level.toUpperCase(),
        message: message
    });
    
    // 限制数量
    if (logs.length > MAX_LOGS) {
        logs = logs.slice(-MAX_LOGS);
    }
    
    renderLogs();
    scrollToBottom();
}

// 处理 WebSocket 消息
function handleWebSocketMessage(data) {
    // 检查是否是批次结束标记
    if (data.type === 'batch_end') {
        console.log(`收到历史日志批次结束标记，批次ID: ${data.batchId}, 数量: ${data.count}`);
        
        // 批量处理历史日志
        if (historyBatch.length > 0) {
            addHistoryLogs(historyBatch);
            historyBatch = [];
        }
        return;
    }
    
    // 处理日志条目
    if (data.time && data.level && data.message) {
        if (data.type === 'history') {
            // 历史日志：临时存储
            historyBatch.push({
                time: data.time,
                level: data.level,
                message: data.message
            });
        } else {
            // 实时日志：立即处理
            addRealtimeLog(data.message, data.level, data.time);
        }
    }
}

// 渲染日志到容器
function renderLogs() {
    const container = document.getElementById('log-container');
    if (!container) return;

    // 清空容器
    container.innerHTML = '';

    // 按时间正序渲染日志（最新的在底部）
    logs.forEach(log => {
        // 应用过滤规则
        if (log.level === 'DEBUG' && !logFilters.DEBUG) return;
        if (log.level === 'INFO' && !logFilters.INFO) return;

        const p = document.createElement('p');

        // 解析日志消息，提取时间戳、级别和消息内容
        const timeMatch = log.time.match(/\[?([^\]]+)\]?/);
        const timeStr = timeMatch ? timeMatch[1] : log.time;

        // 创建带样式的日志内容
        const timeSpan = document.createElement('span');
        timeSpan.className = 'log-time';
        timeSpan.textContent = `[${timeStr}] `;

        const levelSpan = document.createElement('span');
        levelSpan.className = `log-level-${log.level}`;
        levelSpan.textContent = `[${log.level}] `;

        const messageSpan = document.createElement('span');
        messageSpan.className = 'log-message';
        messageSpan.textContent = log.message;

        // 根据消息内容添加特殊样式
        if (log.message.includes('✅') || log.message.includes('完成') && log.level === 'INFO') {
            messageSpan.style.color = '#f0f0f0'; // 较亮的白色
        } else if (log.message.includes('✗') || log.level === 'ERROR') {
            messageSpan.style.color = '#ff6b6b'; // 错误消息用稍亮的红色
        }

        p.appendChild(timeSpan);
        p.appendChild(levelSpan);
        p.appendChild(messageSpan);

        container.appendChild(p);
    });
}

// 滚动到底部
function scrollToBottom() {
    const container = document.getElementById('log-container');
    if (container) {
        container.scrollTop = container.scrollHeight;
    }
}

function clearLogs() {
    logs = [];
    historyBatch = [];
    const container = document.getElementById('log-container');
    if (container) {
        container.innerHTML = '';
    }
}

// 切换日志过滤
function toggleLogFilter(level) {
    logFilters[level] = !logFilters[level];
    
    // 更新按钮样式
    const btn = document.getElementById(`log-filter-${level.toLowerCase()}`);
    if (btn) {
        btn.classList.toggle('active', logFilters[level]);
    }
    
    // 重新渲染日志
    renderLogs();
}

async function api(method, url, data) {
    const options = {
        method,
        headers: { 'Content-Type': 'application/json' }
    };
    if (data) {
        options.body = JSON.stringify(data);
    }
    const response = await fetch(url, options);
    return response.json();
}

async function loadTasks() {
    const result = await api('GET', '/api/tasks');
    const tbody = document.getElementById('tasks-list');
    tbody.innerHTML = '';

    (result.data || []).forEach(task => {
        const scheduleText = formatSchedule(task.schedule);
        const typeLabel = task.type === 'nodeimage' ? 'NodeImage' : '本地备份';
        const statusClass = task.enabled ? 'status-enabled' : 'status-disabled';
        const statusText = task.enabled ? '已启用' : '已禁用';

        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td>${task.name}</td>
            <td>${typeLabel}</td>
            <td>${scheduleText}</td>
            <td><span class="status-badge ${statusClass}">${statusText}</span></td>
            <td>
                <button class="btn-primary btn-small" onclick="runTask('${task.name}')">运行</button>
                <button class="btn-secondary btn-small" onclick="editTask('${task.name}')">编辑</button>
                <button class="btn-danger btn-small" onclick="deleteTask('${task.name}')">删除</button>
            </td>
        `;
        tbody.appendChild(tr);
    });
}

async function loadWebDAV() {
    const result = await api('GET', '/api/webdav');
    webdavServers = result.data || [];
    const tbody = document.getElementById('webdav-list');
    tbody.innerHTML = '';

    webdavServers.forEach(wd => {
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td>${wd.name}</td>
            <td>${wd.url}</td>
            <td>
                <button class="btn-primary btn-small" onclick="testWebdav('${wd.name}')">测试</button>
                <button class="btn-secondary btn-small" onclick="editWebdav('${wd.name}')">编辑</button>
                <button class="btn-danger btn-small" onclick="deleteWebdav('${wd.name}')">删除</button>
            </td>
        `;
        tbody.appendChild(tr);
    });
}

function formatDateTime(dateStr) {
    if (!dateStr || dateStr === '' || dateStr === '0001-01-01T00:00:00Z') {
        return '-';
    }
    if (dateStr === '已禁用') {
        return '<span style="color: var(--subtle-text-color);">已禁用</span>';
    }
    const date = new Date(dateStr);
    if (isNaN(date.getTime())) {
        return dateStr;
    }
    const now = new Date();
    const diff = date - now;
    const absDiff = Math.abs(diff);
    
    // 如果是未来时间，显示相对时间
    if (diff > 0) {
        const minutes = Math.floor(absDiff / 60000);
        const hours = Math.floor(absDiff / 3600000);
        const days = Math.floor(absDiff / 86400000);
        
        if (minutes < 1) return '即将';
        if (minutes < 60) return `${minutes} 分钟后`;
        if (hours < 24) return `${hours} 小时后`;
        if (days < 7) return `${days} 天后`;
    }
    
    // 否则显示具体日期时间
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, '0');
    const day = String(date.getDate()).padStart(2, '0');
    const hour = String(date.getHours()).padStart(2, '0');
    const minute = String(date.getMinutes()).padStart(2, '0');
    
    const nowYear = now.getFullYear();
    const nowMonth = now.getMonth();
    const nowDay = now.getDate();
    
    // 今天
    if (year === nowYear && date.getMonth() === nowMonth && day === String(nowDay).padStart(2, '0')) {
        return `今天 ${hour}:${minute}`;
    }
    // 明天
    const tomorrow = new Date(now);
    tomorrow.setDate(tomorrow.getDate() + 1);
    if (year === tomorrow.getFullYear() && date.getMonth() === tomorrow.getMonth() && day === String(tomorrow.getDate()).padStart(2, '0')) {
        return `明天 ${hour}:${minute}`;
    }
    // 今年
    if (year === nowYear) {
        return `${month}-${day} ${hour}:${minute}`;
    }
    return `${year}-${month}-${day} ${hour}:${minute}`;
}

async function loadStatus() {
    const result = await api('GET', '/api/status');
    const tbody = document.getElementById('status-list');
    tbody.innerHTML = '';

    (result.data || []).forEach(item => {
        const tr = document.createElement('tr');
        let execStatus = '-';
        if (item.execution && item.execution.status !== 'running') {
            const statusMap = {
                'success': '成功',
                'failed': '失败'
            };
            execStatus = statusMap[item.execution.status] || item.execution.status;
            if (item.execution.error) {
                execStatus += ` (${item.execution.error})`;
            }
        }
        const enabledClass = item.enabled ? '' : 'style="color: var(--subtle-text-color);"';
        tr.innerHTML = `
            <td ${enabledClass}>${item.name}</td>
            <td>${formatDateTime(item.last_run)}</td>
            <td>${formatDateTime(item.next_run)}</td>
            <td>${execStatus}</td>
        `;
        tbody.appendChild(tr);
    });
}

function formatSchedule(schedule) {
    if (!schedule) return '-';
    const minute = String(schedule.minute || 0).padStart(2, '0');
    switch (schedule.type) {
        case 'hourly':
            return `每小时 :${minute}`;
        case 'daily':
            return `每天 ${schedule.hour || 0}:${minute}`;
        case 'weekly':
            const days = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
            return `${days[schedule.day || 0]} ${schedule.hour || 0}:${minute}`;
        default:
            return '-';
    }
}

function showTaskModal(editData = null) {
    document.getElementById('task-modal-title').textContent = editData ? '编辑任务' : '添加任务';
    document.getElementById('task-form').reset();
    document.getElementById('task-original-name').value = editData ? editData.name : '';
    document.getElementById('task-type').value = 'local';
    document.getElementById('task-enabled').checked = true;
    
    if (editData) {
        document.getElementById('task-name').value = editData.name;
        document.getElementById('task-enabled').checked = editData.enabled !== false;
        document.getElementById('task-type').value = editData.type || 'local';
        
        if (editData.type === 'nodeimage') {
            document.getElementById('task-sync-mode').value = editData.sync_mode || 'incremental';
            document.getElementById('task-api-key').value = editData.nodeimage?.api_key || '';
            document.getElementById('task-cookie').value = editData.nodeimage?.cookie || '';
            document.getElementById('task-base-path').value = editData.nodeimage?.base_path || '';
            updateSyncModeHints();
        } else {
            document.getElementById('task-paths').value = (editData.paths || []).map(p => p.path).join('\n');
            // 加载所有排除路径（从第一个路径项获取，或者汇总所有路径项的排除路径）
            const allExcludePaths = new Set();
            (editData.paths || []).forEach(p => {
                (p.exclude_paths || []).forEach(ep => allExcludePaths.add(ep));
            });
            document.getElementById('task-exclude-paths').value = Array.from(allExcludePaths).join('\n');
            document.getElementById('task-encrypt-pwd').value = editData.encrypt_pwd || '';
            document.getElementById('task-local-base-path').value = editData.base_path || '';
        }
        
        document.getElementById('task-schedule-type').value = editData.schedule?.type || 'daily';
        document.getElementById('task-hour').value = editData.schedule?.hour || 0;
        document.getElementById('task-minute').value = editData.schedule?.minute || 0;
        document.getElementById('task-day').value = editData.schedule?.day || 1;
        
        renderWebdavCheckboxes(editData.webdav || []);
    } else {
        renderWebdavCheckboxes([]);
    }
    
    updateTaskFormFields();
    updateScheduleFields();
    document.getElementById('task-modal').classList.add('show');
}

function showWebdavModal(editData = null) {
    document.getElementById('webdav-modal-title').textContent = editData ? '编辑 WebDAV 服务器' : '添加 WebDAV 服务器';
    document.getElementById('webdav-form').reset();
    document.getElementById('webdav-original-name').value = editData ? editData.name : '';
    
    if (editData) {
        document.getElementById('webdav-name').value = editData.name;
        document.getElementById('webdav-url').value = editData.url;
        document.getElementById('webdav-username').value = editData.username || '';
        document.getElementById('webdav-timeout').value = editData.timeout || 300;
    }
    
    document.getElementById('webdav-modal').classList.add('show');
}

function renderWebdavCheckboxes(selected) {
    const container = document.getElementById('webdav-checkboxes');
    container.innerHTML = '';
    
    if (!webdavServers.length) {
        container.innerHTML = '<span style="color: var(--subtle-text-color); font-size: 0.85rem;">未配置 WebDAV 服务器</span>';
        return;
    }
    
    webdavServers.forEach(s => {
        const label = document.createElement('label');
        const checkbox = document.createElement('input');
        checkbox.type = 'checkbox';
        checkbox.value = s.name;
        checkbox.checked = (selected || []).includes(s.name);
        label.appendChild(checkbox);
        label.appendChild(document.createTextNode(' ' + s.name));
        container.appendChild(label);
    });
}

function updateTaskFormFields() {
    const type = document.getElementById('task-type').value;
    document.getElementById('local-task-fields').classList.toggle('hidden', type !== 'local');
    document.getElementById('nodeimage-task-fields').classList.toggle('hidden', type !== 'nodeimage');
}

function updateScheduleFields() {
    const type = document.getElementById('task-schedule-type').value;
    document.getElementById('day-group').classList.toggle('hidden', type !== 'weekly');
    document.getElementById('hour-group').classList.toggle('hidden', type === 'hourly');
}

function updateSyncModeHints() {
    const syncMode = document.getElementById('task-sync-mode').value;
    const apikeyHint = document.getElementById('apikey-hint');
    const cookieHint = document.getElementById('cookie-hint');
    
    if (syncMode === 'incremental') {
        apikeyHint.textContent = '(必需)';
        apikeyHint.style.color = 'var(--danger-color)';
        cookieHint.textContent = '(可选)';
        cookieHint.style.color = 'var(--subtle-text-color)';
    } else {
        apikeyHint.textContent = '(可选)';
        apikeyHint.style.color = 'var(--subtle-text-color)';
        cookieHint.textContent = '(必需)';
        cookieHint.style.color = 'var(--danger-color)';
    }
}

async function handleTaskSubmit(e) {
    e.preventDefault();
    
    const originalName = document.getElementById('task-original-name').value;
    const taskType = document.getElementById('task-type').value;
    const webdavSelected = Array.from(document.querySelectorAll('#webdav-checkboxes input:checked')).map(cb => cb.value);
    
    const taskData = {
        name: document.getElementById('task-name').value,
        type: taskType,
        enabled: document.getElementById('task-enabled').checked,
        webdav: webdavSelected,
        schedule: {
            type: document.getElementById('task-schedule-type').value,
            hour: parseInt(document.getElementById('task-hour').value),
            minute: parseInt(document.getElementById('task-minute').value),
            day: parseInt(document.getElementById('task-day').value)
        }
    };
    
    if (taskType === 'nodeimage') {
        taskData.sync_mode = document.getElementById('task-sync-mode').value;
        taskData.nodeimage = {
            api_key: document.getElementById('task-api-key').value,
            cookie: document.getElementById('task-cookie').value,
            base_path: document.getElementById('task-base-path').value
        };
    } else {
        const excludePaths = document.getElementById('task-exclude-paths').value.split('\n').filter(p => p.trim()).map(p => p.trim());
        taskData.paths = document.getElementById('task-paths').value.split('\n').filter(p => p.trim()).map(p => ({ 
            path: p.trim(),
            exclude_paths: excludePaths
        }));
        taskData.encrypt_pwd = document.getElementById('task-encrypt-pwd').value;
        taskData.base_path = document.getElementById('task-local-base-path').value;
    }
    
    try {
        if (originalName) {
            await api('PUT', `/api/tasks/${originalName}`, taskData);
        } else {
            await api('POST', '/api/tasks', taskData);
        }
        document.getElementById('task-modal').classList.remove('show');
        await loadTasks();
        showToast('success', '保存成功', `任务 "${taskData.name}" 已保存`, 3000);
    } catch (error) {
        console.error('Failed to save task:', error);
        showToast('error', '保存失败', '无法保存任务', 5000);
    }
}

async function handleWebdavSubmit(e) {
    e.preventDefault();
    
    const originalName = document.getElementById('webdav-original-name').value;
    const webdavData = {
        name: document.getElementById('webdav-name').value,
        url: document.getElementById('webdav-url').value,
        username: document.getElementById('webdav-username').value,
        password: document.getElementById('webdav-password').value,
        timeout: parseInt(document.getElementById('webdav-timeout').value)
    };
    
    try {
        if (originalName) {
            await api('PUT', `/api/webdav/${originalName}`, webdavData);
        } else {
            await api('POST', '/api/webdav', webdavData);
        }
        document.getElementById('webdav-modal').classList.remove('show');
        await loadWebDAV();
        showToast('success', '保存成功', `服务器 "${webdavData.name}" 已保存`, 3000);
    } catch (error) {
        console.error('Failed to save webdav:', error);
        showToast('error', '保存失败', '无法保存服务器配置', 5000);
    }
}

async function runTask(name) {
    try {
        const result = await api('POST', `/api/tasks/${name}/run`);
        if (result.success) {
            // 显示成功的 Toast 通知
            showToast('success', '任务启动成功', `任务 "${name}" 已开始执行`, 3000);
            
            // 自动跳转到实时日志标签页
            setTimeout(() => {
                const logsTab = document.querySelector('.tab-btn[data-tab="logs"]');
                if (logsTab) {
                    logsTab.click();
                }
            }, 500);
        } else {
            showToast('error', '启动失败', result.message || '未知错误', 5000);
        }
    } catch (error) {
        console.error('Failed to run task:', error);
        showToast('error', '启动失败', '无法连接到服务器', 5000);
    }
}

async function editTask(name) {
    try {
        const result = await api('GET', `/api/tasks/${name}`);
        if (result.success) {
            showTaskModal(result.data);
        }
    } catch (error) {
        console.error('Failed to load task:', error);
    }
}

async function deleteTask(name) {
    const confirmed = await showConfirm('确认删除任务', `您确定要删除任务 "${name}" 吗？此操作无法撤销。`);
    if (!confirmed) return;
    
    try {
        await api('DELETE', `/api/tasks/${name}`);
        await loadTasks();
        showToast('success', '删除成功', `任务 "${name}" 已删除`, 3000);
    } catch (error) {
        console.error('Failed to delete task:', error);
        showToast('error', '删除失败', '无法删除任务', 5000);
    }
}

async function testWebdav(name) {
    try {
        const result = await api('POST', `/api/webdav/${name}/test`);
        if (result.success) {
            showToast('success', '连接成功', result.message, 3000);
        } else {
            showToast('error', '连接失败', result.message, 5000);
        }
    } catch (error) {
        console.error('Failed to test webdav:', error);
        showToast('error', '测试失败', '无法连接到服务器', 5000);
    }
}

async function editWebdav(name) {
    try {
        const result = await api('GET', `/api/webdav/${name}`);
        if (result.success) {
            showWebdavModal(result.data);
        }
    } catch (error) {
        console.error('Failed to load webdav:', error);
    }
}

async function deleteWebdav(name) {
    const confirmed = await showConfirm('确认删除服务器', `您确定要删除服务器 "${name}" 吗？此操作无法撤销。`);
    if (!confirmed) return;
    
    try {
        await api('DELETE', `/api/webdav/${name}`);
        await loadWebDAV();
        showToast('success', '删除成功', `服务器 "${name}" 已删除`, 3000);
    } catch (error) {
        console.error('Failed to delete webdav:', error);
        showToast('error', '删除失败', '无法删除服务器', 5000);
    }
}
