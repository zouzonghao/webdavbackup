package backup

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"webdav-backup/config"
	"webdav-backup/logger"
	"webdav-backup/webdav"
)

type Executor struct {
	config *config.Config
}

func NewExecutor(cfg *config.Config) *Executor {
	return &Executor{config: cfg}
}

func (e *Executor) Execute(task *config.BackupTask) error {
	logger.Info("[%s] Starting backup task", task.Name)

	var memStatsStop = make(chan struct{})
	go func() {
		for {
			select {
			case <-memStatsStop:
				return
			case <-time.After(1 * time.Second):
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				logger.Info("[%s] Memory - Alloc: %.2f MB, Sys: %.2f MB",
					task.Name,
					float64(m.Alloc)/1024/1024,
					float64(m.Sys)/1024/1024)
			}
		}
	}()
	defer close(memStatsStop)

	if len(task.Paths) == 0 {
		return fmt.Errorf("no backup paths configured")
	}

	logger.Info("[%s] Validating paths...", task.Name)
	var validPaths []config.BackupItem
	for _, item := range task.Paths {
		info, err := os.Stat(item.Path)
		if err != nil {
			logger.Warn("[%s] Path not accessible, skipping: %s (%v)", task.Name, item.Path, err)
			continue
		}
		validPaths = append(validPaths, config.BackupItem{Path: item.Path})
		if info.IsDir() {
			logger.Info("[%s] Path validated: %s (dir)", task.Name, item.Path)
		} else {
			logger.Info("[%s] Path validated: %s (file)", task.Name, item.Path)
		}
	}

	if len(validPaths) == 0 {
		return fmt.Errorf("no valid backup paths found")
	}

	taskCopy := *task
	taskCopy.Paths = validPaths

	timestamp := time.Now().Format("20060102_150405")
	remotePath := fmt.Sprintf("%s_%s.tar.gz", taskCopy.Name, timestamp)

	var uploadErrors []string
	for _, webdavName := range taskCopy.WebDAV {
		wdCfg := e.config.GetWebDAVByName(webdavName)
		if wdCfg == nil {
			uploadErrors = append(uploadErrors, fmt.Sprintf("%s: not found in config", webdavName))
			continue
		}

		client := webdav.NewClient(webdav.Config{
			Name:     wdCfg.Name,
			URL:      wdCfg.URL,
			Username: wdCfg.Username,
			Password: wdCfg.Password,
			Timeout:  wdCfg.Timeout,
		})

		logger.Info("[%s] Testing connection to WebDAV server: %s", taskCopy.Name, wdCfg.Name)
		if err := client.TestConnection(); err != nil {
			uploadErrors = append(uploadErrors, fmt.Sprintf("%s: connection failed: %v", wdCfg.Name, err))
			continue
		}

		backupSvc := New()
		result, err := backupSvc.CreateStream(&taskCopy)
		if err != nil {
			uploadErrors = append(uploadErrors, fmt.Sprintf("%s: failed to create stream: %v", wdCfg.Name, err))
			continue
		}

		logger.Info("[%s] Uploading to %s as %s (%.2f MB)", taskCopy.Name, wdCfg.Name, remotePath, float64(result.TotalSize)/1024/1024)

		if err := client.UploadStream(result.Stream, result.TotalSize, remotePath); err != nil {
			result.Stream.Close()
			uploadErrors = append(uploadErrors, fmt.Sprintf("%s: upload failed: %v", wdCfg.Name, err))
			continue
		}
		result.Stream.Close()

		logger.Info("[%s] Backup uploaded successfully to %s", taskCopy.Name, wdCfg.Name)
	}

	if len(uploadErrors) > 0 {
		return fmt.Errorf("upload errors: %v", uploadErrors)
	}

	logger.Info("[%s] Backup task completed successfully", taskCopy.Name)
	return nil
}
