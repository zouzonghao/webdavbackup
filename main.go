package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"webdav-backup/config"
	"webdav-backup/engine"
	"webdav-backup/logger"
	"webdav-backup/scheduler"
	"webdav-backup/webserver"
)

//go:embed public/*
var staticFiles embed.FS

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

func getStaticFS() fs.FS {
	sub, err := fs.Sub(staticFiles, "public")
	if err != nil {
		return nil
	}
	return sub
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

	executor := engine.NewExecutor(cfg)

	taskFunc := func(task interface{}) error {
		return executor.ExecuteTask(task)
	}

	if *runOnce {
		if *taskName != "" {
			task := cfg.GetTaskByName(*taskName)
			if task == nil {
				logger.Error("Task not found: %s", *taskName)
				os.Exit(1)
			}
			if err := executor.ExecuteTask(task); err != nil {
				logger.Error("Task execution failed: %v", err)
				os.Exit(1)
			}
		} else {
			hasError := false
			for i := range cfg.LocalTasks {
				task := &cfg.LocalTasks[i]
				if !task.Enabled {
					logger.Info("[本地备份] Skipping disabled task: %s", task.Name)
					continue
				}
				if err := executor.ExecuteTask(task); err != nil {
					logger.Error("[本地备份] Task '%s' failed: %v", task.Name, err)
					hasError = true
				}
			}
			for i := range cfg.NodeImageTasks {
				task := &cfg.NodeImageTasks[i]
				if !task.Enabled {
					logger.Info("[NodeImage] Skipping disabled task: %s", task.Name)
					continue
				}
				if err := executor.ExecuteTask(task); err != nil {
					logger.Error("[NodeImage] Task '%s' failed: %v", task.Name, err)
					hasError = true
				}
			}
			if hasError {
				os.Exit(1)
			}
		}
	} else {
		runDaemon(cfg, taskFunc)
	}
}

func listAllTasks(cfg *config.Config) {
	fmt.Println("Backup Tasks:")
	fmt.Println("==========================================")

	fmt.Println("本地备份任务:")
	for _, task := range cfg.LocalTasks {
		status := "disabled"
		if task.Enabled {
			status = "enabled"
		}
		fmt.Printf("  [%s] %s (%s)\n", status, task.Name, task.Schedule.String())
		fmt.Printf("    类型: 本地备份\n")
		fmt.Printf("    Paths: %d items\n", len(task.Paths))
		fmt.Printf("    WebDAV: %v\n", task.WebDAV)
		fmt.Println()
	}

	fmt.Println("NodeImage同步任务:")
	for _, task := range cfg.NodeImageTasks {
		status := "disabled"
		if task.Enabled {
			status = "enabled"
		}
		fmt.Printf("  [%s] %s (%s)\n", status, task.Name, task.Schedule.String())
		fmt.Printf("    类型: NodeImage同步\n")
		fmt.Printf("    WebDAV: %v\n", task.WebDAV)
		fmt.Printf("    并发数: %d\n", task.Concurrency)
		fmt.Println()
	}
}

func runDaemon(cfg *config.Config, taskFunc scheduler.TaskFunc) {
	logger.Info("Starting daemon mode")

	server := webserver.NewServer(cfg, taskFunc, getStaticFS())

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
