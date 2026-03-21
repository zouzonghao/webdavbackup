package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"webdav-backup/backup"
	"webdav-backup/config"
	"webdav-backup/logger"
	"webdav-backup/webdav"
	"webdav-backup/webserver"
)

var (
	configPath  = flag.String("config", "", "Path to config file (default: ./config.yaml)")
	taskName    = flag.String("task", "", "Task name to run (empty for all enabled tasks)")
	listTasks   = flag.Bool("list", false, "List all tasks")
	version     = "1.0.0"
	showVersion = flag.Bool("version", false, "Show version")
	runOnce     = flag.Bool("run", false, "Run tasks once and exit (no web server)")
)

func getConfigPath() string {
	if *configPath != "" {
		return *configPath
	}
	wd, err := os.Getwd()
	if err != nil {
		return "config.yaml"
	}
	return filepath.Join(wd, "config.yaml")
}

func main() {
	flag.Parse()

	if *showVersion {
		fmt.Printf("webdav-backup version %s\n", version)
		os.Exit(0)
	}

	cfgPath := getConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	config.SetConfigPath(cfgPath)
	logger.Init()

	if *listTasks {
		listAllTasks(cfg)
		os.Exit(0)
	}

	if *runOnce {
		if *taskName != "" {
			task := cfg.GetTaskByName(*taskName)
			if task == nil {
				logger.Error("Task not found: %s", *taskName)
				os.Exit(1)
			}
			runTask(cfg, task)
		} else {
			runAllEnabledTasks(cfg)
		}
	} else {
		runDaemon(cfg)
	}
}

func listAllTasks(cfg *config.Config) {
	fmt.Println("Backup Tasks:")
	fmt.Println("==========================================")
	for _, task := range cfg.Tasks {
		status := "disabled"
		if task.Enabled {
			status = "enabled"
		}
		fmt.Printf("  [%s] %s (%s)\n", status, task.Name, task.Schedule.String())
		fmt.Printf("    Paths: %d items\n", len(task.Paths))
		fmt.Printf("    WebDAV: %v\n", task.WebDAV)
		fmt.Println()
	}
}

func runDaemon(cfg *config.Config) {
	logger.Info("Starting daemon mode")

	server := webserver.NewServer(cfg, func(task *config.BackupTask) error {
		return runTaskWithError(cfg, task)
	})

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info("Shutting down...")
		os.Exit(0)
	}()

	if err := server.Start(); err != nil {
		logger.Error("Web server error: %v", err)
		os.Exit(1)
	}
}

func runAllEnabledTasks(cfg *config.Config) {
	hasError := false
	for i := range cfg.Tasks {
		task := &cfg.Tasks[i]
		if !task.Enabled {
			logger.Info("[%s] Skipping disabled task", task.Name)
			continue
		}
		if err := runTaskWithError(cfg, task); err != nil {
			logger.Error("[%s] Task failed: %v", task.Name, err)
			hasError = true
		}
	}
	if hasError {
		os.Exit(1)
	}
}

func runTask(cfg *config.Config, task *config.BackupTask) {
	if err := runTaskWithError(cfg, task); err != nil {
		logger.Error("[%s] Task failed: %v", task.Name, err)
		os.Exit(1)
	}
}

func runTaskWithError(cfg *config.Config, task *config.BackupTask) error {
	logger.Info("[%s] Starting backup task", task.Name)

	if len(task.Paths) == 0 {
		return fmt.Errorf("no backup paths configured")
	}

	logger.Info("[%s] Pre-flight check: validating paths", task.Name)
	var validPaths []config.BackupItem
	for _, item := range task.Paths {
		info, err := os.Stat(item.Path)
		if err != nil {
			logger.Warn("[%s] Path not accessible, skipping: %s (%v)", task.Name, item.Path, err)
			continue
		}
		validItem := config.BackupItem{Path: item.Path}
		if item.Type == "" {
			if info.IsDir() {
				validItem.Type = "dir"
			} else {
				validItem.Type = "file"
			}
		} else {
			validItem.Type = item.Type
		}
		validPaths = append(validPaths, validItem)
		logger.Info("[%s] Path validated: %s (%s)", task.Name, item.Path, validItem.Type)
	}

	if len(validPaths) == 0 {
		return fmt.Errorf("no valid backup paths found")
	}

	task.Paths = validPaths

	backupSvc := backup.New(cfg.TempDir)

	backupFile, err := backupSvc.CreateTask(task, cfg.Encryption.Password)
	if err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}

	defer func() {
		logger.Info("[%s] Cleaning up temp file: %s", task.Name, backupFile)
		if err := os.Remove(backupFile); err != nil {
			logger.Warn("[%s] Failed to remove temp file: %v", task.Name, err)
		}
	}()

	timestamp := time.Now().Format("20060102_150405.000")

	var uploadErrors []string
	for _, webdavName := range task.WebDAV {
		wdCfg := cfg.GetWebDAVByName(webdavName)
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

		logger.Info("[%s] Testing connection to WebDAV server: %s", task.Name, wdCfg.Name)
		if err := client.TestConnection(); err != nil {
			uploadErrors = append(uploadErrors, fmt.Sprintf("%s: connection failed: %v", wdCfg.Name, err))
			continue
		}

		remotePath := fmt.Sprintf("%s_%s.tar.gz.enc", task.Name, timestamp)
		remotePath = filepath.Base(remotePath)

		logger.Info("[%s] Uploading to %s as %s", task.Name, wdCfg.Name, remotePath)

		if err := client.Upload(backupFile, remotePath); err != nil {
			uploadErrors = append(uploadErrors, fmt.Sprintf("%s: upload failed: %v", wdCfg.Name, err))
			continue
		}

		logger.Info("[%s] Backup uploaded successfully to %s", task.Name, wdCfg.Name)
	}

	if len(uploadErrors) > 0 {
		return fmt.Errorf("upload errors: %v", uploadErrors)
	}

	logger.Info("[%s] Backup task completed successfully", task.Name)
	return nil
}
