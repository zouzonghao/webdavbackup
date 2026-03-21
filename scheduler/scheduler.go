package scheduler

import (
	"fmt"
	"sync"
	"time"

	"webdav-backup/config"
	"webdav-backup/logger"
)

type TaskFunc func(task *config.BackupTask) error

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
	task     *config.BackupTask
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

	for i := range cfg.Tasks {
		task := &cfg.Tasks[i]
		if task.Enabled {
			s.scheduleTaskLocked(task)
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

	for i := range cfg.Tasks {
		task := &cfg.Tasks[i]
		if task.Enabled {
			s.scheduleTaskLocked(task)
		}
	}

	logger.Info("Scheduler reloaded with %d tasks", len(s.tasks))
}

func (s *Scheduler) scheduleTaskLocked(task *config.BackupTask) {
	if _, exists := s.tasks[task.Name]; exists {
		return
	}

	st := &scheduledTask{
		task:     task,
		stopChan: make(chan struct{}),
	}

	st.nextRun = calculateNextRun(&task.Schedule)

	go s.runScheduledTask(st)

	s.tasks[task.Name] = st
	logger.Info("[%s] Scheduled: %s, next run: %s", task.Name, task.Schedule.String(), st.nextRun.Format("2006-01-02 15:04:05"))
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
			st.nextRun = calculateNextRun(&st.task.Schedule)
			logger.Info("[%s] Next run: %s", st.task.Name, st.nextRun.Format("2006-01-02 15:04:05"))
			timer.Reset(time.Until(st.nextRun))
		}
	}
}

func (s *Scheduler) executeTask(st *scheduledTask) {
	s.runningMu.Lock()
	if s.runningTasks[st.task.Name] {
		s.runningMu.Unlock()
		logger.Warn("[%s] Task is already running, skipping", st.task.Name)
		return
	}
	s.runningTasks[st.task.Name] = true
	s.runningMu.Unlock()

	defer func() {
		s.runningMu.Lock()
		delete(s.runningTasks, st.task.Name)
		s.runningMu.Unlock()
	}()

	logger.Info("[%s] Executing scheduled task", st.task.Name)

	status := &ExecutionStatus{
		TaskName:  st.task.Name,
		Status:    "running",
		StartTime: time.Now(),
	}
	s.setExecutionStatus(status)

	if s.taskFunc != nil {
		if err := s.taskFunc(st.task); err != nil {
			logger.Error("[%s] Task execution failed: %v", st.task.Name, err)
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

func (s *Scheduler) AddTask(task *config.BackupTask) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if st, exists := s.tasks[task.Name]; exists {
		close(st.stopChan)
		delete(s.tasks, task.Name)
	}

	if task.Enabled && s.isRunning {
		s.scheduleTaskLocked(task)
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

func (s *Scheduler) RunTaskByName(task *config.BackupTask) error {
	s.runningMu.Lock()
	if s.runningTasks[task.Name] {
		s.runningMu.Unlock()
		return fmt.Errorf("task %s is already running", task.Name)
	}
	s.runningTasks[task.Name] = true
	s.runningMu.Unlock()

	defer func() {
		s.runningMu.Lock()
		delete(s.runningTasks, task.Name)
		s.runningMu.Unlock()
	}()

	logger.Info("[%s] Manual execution triggered", task.Name)

	status := &ExecutionStatus{
		TaskName:  task.Name,
		Status:    "running",
		StartTime: time.Now(),
	}
	s.setExecutionStatus(status)

	var execErr error
	if s.taskFunc != nil {
		if err := s.taskFunc(task); err != nil {
			logger.Error("[%s] Task execution failed: %v", task.Name, err)
			status.Status = "failed"
			status.Error = err.Error()
			execErr = err
		} else {
			status.Status = "success"
		}
	} else {
		status.Status = "success"
	}

	status.EndTime = time.Now()
	s.setExecutionStatus(status)

	return execErr
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

	return &TaskStatus{
		Name:     st.task.Name,
		Enabled:  st.task.Enabled,
		Schedule: st.task.Schedule.String(),
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
		statuses = append(statuses, &TaskStatus{
			Name:     st.task.Name,
			Enabled:  st.task.Enabled,
			Schedule: st.task.Schedule.String(),
			LastRun:  st.lastRun,
			NextRun:  st.nextRun,
			Running:  s.runningTasks[st.task.Name],
		})
	}
	return statuses
}

type TaskStatus struct {
	Name     string    `json:"name"`
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
