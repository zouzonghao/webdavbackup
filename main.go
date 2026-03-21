package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"webdav-backup/backup"
	"webdav-backup/config"
	"webdav-backup/logger"
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
	logger.Init()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Error("Failed to load config: %v", err)
		os.Exit(1)
	}

	config.SetConfigPath(cfgPath)

	if *listTasks {
		listAllTasks(cfg)
		os.Exit(0)
	}

	executor := backup.NewExecutor(cfg)

	if *runOnce {
		if *taskName != "" {
			task := cfg.GetTaskByName(*taskName)
			if task == nil {
				logger.Error("Task not found: %s", *taskName)
				os.Exit(1)
			}
			if err := executor.Execute(task); err != nil {
				logger.Error("[%s] Task failed: %v", task.Name, err)
				os.Exit(1)
			}
		} else {
			hasError := false
			for i := range cfg.Tasks {
				task := &cfg.Tasks[i]
				if !task.Enabled {
					logger.Info("[%s] Skipping disabled task", task.Name)
					continue
				}
				if err := executor.Execute(task); err != nil {
					logger.Error("[%s] Task failed: %v", task.Name, err)
					hasError = true
				}
			}
			if hasError {
				os.Exit(1)
			}
		}
	} else {
		runDaemon(cfg, executor)
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

func runDaemon(cfg *config.Config, executor *backup.Executor) {
	logger.Info("Starting daemon mode")

	server := webserver.NewServer(cfg, executor.Execute)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Start()
	}()

	select {
	case <-sigChan:
		logger.Info("Shutting down...")
		server.Stop()
		logger.Info("Shutdown complete")
	case err := <-serverErr:
		if err != nil {
			logger.Error("Web server error: %v", err)
			os.Exit(1)
		}
	}
}
