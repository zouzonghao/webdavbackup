package scheduler

import (
	"fmt"
	"sync"
	"time"

	"webdav-backup/config"
	"webdav-backup/logger"
)

// TaskFunc 现在支持两种任务类型
type TaskFunc func(task interface{}) error

type ExecutionStatus struct {
	TaskName  string    `json:"task_name"`
	Status    string    `json:"status"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	Error     string    `json:"error,omitempty"`
}

type Scheduler struct {
	tasks          map[string]*scheduledTask
	mu             sync.RWMutex
	taskFunc       TaskFunc
	isRunning      bool
	executionMu    sync.RWMutex
	executionStore map[string]*ExecutionStatus
	runningMu      sync.RWMutex
	runningTasks   map[string]bool
}

type scheduledTask struct {
	task     interface{} // 可以是 *config.LocalBackupTask 或 *config.NodeImageSyncTask
	taskName string
	taskType string // "local" 或 "nodeimage"
	stopChan chan struct{}
	lastRun  time.Time
	nextRun  time.Time
}

func NewScheduler(taskFunc TaskFunc) *Scheduler {
	return &Scheduler{
		tasks:          make(map[string]*scheduledTask),
		taskFunc:       taskFunc,
		executionStore: make(map[string]*ExecutionStatus),
		runningTasks:   make(map[string]bool),
	}
}

func (s *Scheduler) Start(cfg *config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isRunning {
		return
	}

	s.isRunning = true

	// 调度本地备份任务
	for i := range cfg.LocalTasks {
		task := &cfg.LocalTasks[i]
		if task.Enabled {
			s.scheduleTaskLocked(task, "local")
		}
	}

	// 调度NodeImage同步任务
	for i := range cfg.NodeImageTasks {
		task := &cfg.NodeImageTasks[i]
		if task.Enabled {
			s.scheduleTaskLocked(task, "nodeimage")
		}
	}

	logger.Info("Scheduler started with %d tasks", len(s.tasks))
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isRunning {
		return
	}

	for name, st := range s.tasks {
		close(st.stopChan)
		delete(s.tasks, name)
	}

	s.isRunning = false
	logger.Info("Scheduler stopped")
}

func (s *Scheduler) Reload(cfg *config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for name, st := range s.tasks {
		close(st.stopChan)
		delete(s.tasks, name)
	}

	// 调度本地备份任务
	for i := range cfg.LocalTasks {
		task := &cfg.LocalTasks[i]
		if task.Enabled {
			s.scheduleTaskLocked(task, "local")
		}
	}

	// 调度NodeImage同步任务
	for i := range cfg.NodeImageTasks {
		task := &cfg.NodeImageTasks[i]
		if task.Enabled {
			s.scheduleTaskLocked(task, "nodeimage")
		}
	}

	logger.Info("Scheduler reloaded with %d tasks", len(s.tasks))
}

func (s *Scheduler) scheduleTaskLocked(task interface{}, taskType string) {
	var taskName string
	var schedule config.ScheduleConfig

	// 提取任务名称和调度配置
	switch t := task.(type) {
	case *config.LocalBackupTask:
		taskName = t.Name
		schedule = t.Schedule
	case *config.NodeImageSyncTask:
		taskName = t.Name
		schedule = t.Schedule
	default:
		logger.Error("不支持的任務類型: %T", task)
		return
	}

	if _, exists := s.tasks[taskName]; exists {
		return
	}

	st := &scheduledTask{
		task:     task,
		taskName: taskName,
		taskType: taskType,
		stopChan: make(chan struct{}),
	}

	st.nextRun = calculateNextRun(&schedule)

	go s.runScheduledTask(st)

	s.tasks[taskName] = st
	logger.Info("[%s] Scheduled: %s, next run: %s", taskName, schedule.String(), st.nextRun.Format("2006-01-02 15:04:05"))
}

func (s *Scheduler) runScheduledTask(st *scheduledTask) {
	duration := time.Until(st.nextRun)
	if duration < 0 {
		duration = 0
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	for {
		select {
		case <-st.stopChan:
			return
		case <-timer.C:
			s.executeTask(st)
			st.lastRun = time.Now()
			
			// 计算下一次运行时间
			var schedule config.ScheduleConfig
			switch t := st.task.(type) {
			case *config.LocalBackupTask:
				schedule = t.Schedule
			case *config.NodeImageSyncTask:
				schedule = t.Schedule
			default:
				logger.Error("不支持的任務類型: %T", st.task)
				continue
			}
			
			st.nextRun = calculateNextRun(&schedule)
			logger.Info("[%s] Next run: %s", st.taskName, st.nextRun.Format("2006-01-02 15:04:05"))
			timer.Reset(time.Until(st.nextRun))
		}
	}
}

func (s *Scheduler) executeTask(st *scheduledTask) {
	if !s.tryStartTask(st.taskName) {
		logger.Warn("[%s] Task is already running, skipping", st.taskName)
		return
	}
	defer s.finishTask(st.taskName)

	logger.Info("[%s] Executing scheduled task", st.taskName)
	s.runTaskWithStatus(st.task)
}

func (s *Scheduler) tryStartTask(name string) bool {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	if s.runningTasks[name] {
		return false
	}
	s.runningTasks[name] = true
	return true
}

func (s *Scheduler) finishTask(name string) {
	s.runningMu.Lock()
	delete(s.runningTasks, name)
	s.runningMu.Unlock()
}

func (s *Scheduler) runTaskWithStatus(task interface{}) {
	var taskName string
	
	// 提取任务名称
	switch t := task.(type) {
	case *config.LocalBackupTask:
		taskName = t.Name
	case *config.NodeImageSyncTask:
		taskName = t.Name
	default:
		logger.Error("不支持的任務類型: %T", task)
		return
	}

	status := &ExecutionStatus{
		TaskName:  taskName,
		Status:    "running",
		StartTime: time.Now(),
	}
	s.setExecutionStatus(status)

	if s.taskFunc != nil {
		if err := s.taskFunc(task); err != nil {
			logger.Error("[%s] Task execution failed: %v", taskName, err)
			status.Status = "failed"
			status.Error = err.Error()
		} else {
			status.Status = "success"
		}
	} else {
		status.Status = "success"
	}

	status.EndTime = time.Now()
	s.setExecutionStatus(status)
}

func (s *Scheduler) setExecutionStatus(status *ExecutionStatus) {
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	s.executionStore[status.TaskName] = status
}

func (s *Scheduler) GetExecutionStatus(name string) *ExecutionStatus {
	s.executionMu.RLock()
	defer s.executionMu.RUnlock()
	return s.executionStore[name]
}

func (s *Scheduler) GetAllExecutionStatus() map[string]*ExecutionStatus {
	s.executionMu.RLock()
	defer s.executionMu.RUnlock()
	result := make(map[string]*ExecutionStatus)
	for k, v := range s.executionStore {
		result[k] = v
	}
	return result
}

func (s *Scheduler) AddTask(task interface{}, taskType string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var taskName string
	var enabled bool

	// 提取任务名称和启用状态
	switch t := task.(type) {
	case *config.LocalBackupTask:
		taskName = t.Name
		enabled = t.Enabled
	case *config.NodeImageSyncTask:
		taskName = t.Name
		enabled = t.Enabled
	default:
		logger.Error("不支持的任務類型: %T", task)
		return
	}

	if st, exists := s.tasks[taskName]; exists {
		close(st.stopChan)
		delete(s.tasks, taskName)
	}

	if enabled && s.isRunning {
		s.scheduleTaskLocked(task, taskType)
	}
}

func (s *Scheduler) RemoveTask(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if st, exists := s.tasks[name]; exists {
		close(st.stopChan)
		delete(s.tasks, name)
		logger.Info("[%s] Task removed from scheduler", name)
	}
}

func (s *Scheduler) RunTaskNow(name string) error {
	s.mu.RLock()
	st, exists := s.tasks[name]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("task not found: %s", name)
	}

	return s.RunTaskByName(st.task)
}

func (s *Scheduler) RunTaskByName(task interface{}) error {
	var taskName string

	// 提取任务名称
	switch t := task.(type) {
	case *config.LocalBackupTask:
		taskName = t.Name
	case *config.NodeImageSyncTask:
		taskName = t.Name
	default:
		return fmt.Errorf("不支持的任務類型: %T", task)
	}

	if !s.tryStartTask(taskName) {
		return fmt.Errorf("task %s is already running", taskName)
	}
	defer s.finishTask(taskName)

	logger.Info("[%s] Manual execution triggered", taskName)
	s.runTaskWithStatus(task)

	status := s.GetExecutionStatus(taskName)
	if status != nil && status.Status == "failed" {
		return fmt.Errorf("%s", status.Error)
	}
	return nil
}

func (s *Scheduler) GetTaskStatus(name string) *TaskStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, exists := s.tasks[name]
	if !exists {
		return nil
	}

	s.runningMu.RLock()
	running := s.runningTasks[name]
	s.runningMu.RUnlock()

	// 提取任务详细信息
	var enabled bool
	var scheduleStr string

	switch t := st.task.(type) {
	case *config.LocalBackupTask:
		enabled = t.Enabled
		scheduleStr = t.Schedule.String()
	case *config.NodeImageSyncTask:
		enabled = t.Enabled
		scheduleStr = t.Schedule.String()
	default:
		return nil
	}

	return &TaskStatus{
		Name:     st.taskName,
		Type:     st.taskType,
		Enabled:  enabled,
		Schedule: scheduleStr,
		LastRun:  st.lastRun,
		NextRun:  st.nextRun,
		Running:  running,
	}
}

func (s *Scheduler) GetAllTaskStatus() []*TaskStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	s.runningMu.RLock()
	defer s.runningMu.RUnlock()

	statuses := make([]*TaskStatus, 0, len(s.tasks))
	for _, st := range s.tasks {
		// 提取任务详细信息
		var enabled bool
		var scheduleStr string

		switch t := st.task.(type) {
		case *config.LocalBackupTask:
			enabled = t.Enabled
			scheduleStr = t.Schedule.String()
		case *config.NodeImageSyncTask:
			enabled = t.Enabled
			scheduleStr = t.Schedule.String()
		default:
			continue
		}

		statuses = append(statuses, &TaskStatus{
			Name:     st.taskName,
			Type:     st.taskType,
			Enabled:  enabled,
			Schedule: scheduleStr,
			LastRun:  st.lastRun,
			NextRun:  st.nextRun,
			Running:  s.runningTasks[st.taskName],
		})
	}
	return statuses
}

type TaskStatus struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Enabled  bool      `json:"enabled"`
	Schedule string    `json:"schedule"`
	LastRun  time.Time `json:"last_run"`
	NextRun  time.Time `json:"next_run"`
	Running  bool      `json:"running"`
}

func calculateNextRun(schedule *config.ScheduleConfig) time.Time {
	now := time.Now()

	switch schedule.Type {
	case "hourly":
		next := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), schedule.Minute, 0, 0, now.Location())
		if next.Before(now) || next.Equal(now) {
			next = next.Add(time.Hour)
		}
		return next

	case "daily":
		next := time.Date(now.Year(), now.Month(), now.Day(), schedule.Hour, schedule.Minute, 0, 0, now.Location())
		if next.Before(now) || next.Equal(now) {
			next = next.Add(24 * time.Hour)
		}
		return next

	case "weekly":
		daysUntilNext := (schedule.Day - int(now.Weekday()) + 7) % 7
		if daysUntilNext == 0 {
			next := time.Date(now.Year(), now.Month(), now.Day(), schedule.Hour, schedule.Minute, 0, 0, now.Location())
			if next.Before(now) || next.Equal(now) {
				daysUntilNext = 7
			}
		}
		next := time.Date(now.Year(), now.Month(), now.Day()+daysUntilNext, schedule.Hour, schedule.Minute, 0, 0, now.Location())
		return next

	default:
		next := time.Date(now.Year(), now.Month(), now.Day(), schedule.Hour, schedule.Minute, 0, 0, now.Location())
		if next.Before(now) || next.Equal(now) {
			next = next.Add(24 * time.Hour)
		}
		return next
	}
}
