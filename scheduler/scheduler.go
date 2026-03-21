package scheduler

import (
	"fmt"
	"sync"
	"time"

	"webdav-backup/config"
	"webdav-backup/logger"
)

type TaskFunc func(task *config.BackupTask) error

type Scheduler struct {
	tasks     map[string]*scheduledTask
	mu        sync.RWMutex
	taskFunc  TaskFunc
	isRunning bool
}

type scheduledTask struct {
	task     *config.BackupTask
	stopChan chan struct{}
	lastRun  time.Time
	nextRun  time.Time
}

func NewScheduler(taskFunc TaskFunc) *Scheduler {
	return &Scheduler{
		tasks:    make(map[string]*scheduledTask),
		taskFunc: taskFunc,
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
			logger.Info("[%s] Executing scheduled task", st.task.Name)

			if s.taskFunc != nil {
				if err := s.taskFunc(st.task); err != nil {
					logger.Error("[%s] Task execution failed: %v", st.task.Name, err)
				}
			}

			st.lastRun = time.Now()
			st.nextRun = calculateNextRun(&st.task.Schedule)

			logger.Info("[%s] Next run: %s", st.task.Name, st.nextRun.Format("2006-01-02 15:04:05"))

			timer.Reset(time.Until(st.nextRun))
		}
	}
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

	logger.Info("[%s] Manual execution triggered", name)

	if s.taskFunc != nil {
		return s.taskFunc(st.task)
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

	return &TaskStatus{
		Name:     st.task.Name,
		Enabled:  st.task.Enabled,
		Schedule: st.task.Schedule.String(),
		LastRun:  st.lastRun,
		NextRun:  st.nextRun,
	}
}

func (s *Scheduler) GetAllTaskStatus() []*TaskStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	statuses := make([]*TaskStatus, 0, len(s.tasks))
	for _, st := range s.tasks {
		statuses = append(statuses, &TaskStatus{
			Name:     st.task.Name,
			Enabled:  st.task.Enabled,
			Schedule: st.task.Schedule.String(),
			LastRun:  st.lastRun,
			NextRun:  st.nextRun,
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
