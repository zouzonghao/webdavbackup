let ws;
let webdavServers = [];

document.addEventListener('DOMContentLoaded', async () => {
    initTabs();
    initModals();
    initForms();
    connectWebSocket();
    await loadTasks();
    await loadWebDAV();
});

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
        addLog('WebSocket 连接成功', 'INFO');
    };

    ws.onmessage = (event) => {
        const data = JSON.parse(event.data);
        addLog(data.message || data.Message, data.level || data.Level || 'INFO');
    };

    ws.onclose = () => {
        addLog('WebSocket 连接断开，3秒后重连...', 'WARN');
        setTimeout(connectWebSocket, 3000);
    };

    ws.onerror = (error) => {
        console.error('WebSocket error:', error);
        addLog('WebSocket 连接错误', 'ERROR');
    };
}

function addLog(message, level) {
    const container = document.getElementById('log-container');
    const p = document.createElement('p');
    p.className = `log-${level.toUpperCase()}`;
    p.textContent = `[${new Date().toLocaleTimeString()}] [${level.toUpperCase()}] ${message}`;
    container.appendChild(p);
    container.scrollTop = container.scrollHeight;
}

function clearLogs() {
    document.getElementById('log-container').innerHTML = '';
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
        taskData.paths = document.getElementById('task-paths').value.split('\n').filter(p => p.trim()).map(p => ({ path: p.trim() }));
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
    } catch (error) {
        console.error('Failed to save task:', error);
        alert('保存失败');
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
    } catch (error) {
        console.error('Failed to save webdav:', error);
        alert('保存失败');
    }
}

async function runTask(name) {
    try {
        const result = await api('POST', `/api/tasks/${name}/run`);
        if (result.success) {
            alert(`任务 ${name} 已开始执行`);
            addLog(`任务 ${name} 开始执行`, 'INFO');
        } else {
            alert(`启动任务失败: ${result.message || '未知错误'}`);
        }
    } catch (error) {
        console.error('Failed to run task:', error);
        alert('启动任务失败');
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
    if (!confirm(`确定删除任务 ${name}？`)) return;
    
    try {
        await api('DELETE', `/api/tasks/${name}`);
        await loadTasks();
    } catch (error) {
        console.error('Failed to delete task:', error);
        alert('删除失败');
    }
}

async function testWebdav(name) {
    try {
        const result = await api('POST', `/api/webdav/${name}/test`);
        alert(result.success ? `连接成功: ${result.message}` : `连接失败: ${result.message}`);
    } catch (error) {
        console.error('Failed to test webdav:', error);
        alert('测试失败');
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
    if (!confirm(`确定删除服务器 ${name}？`)) return;
    
    try {
        await api('DELETE', `/api/webdav/${name}`);
        await loadWebDAV();
    } catch (error) {
        console.error('Failed to delete webdav:', error);
        alert('删除失败');
    }
}
