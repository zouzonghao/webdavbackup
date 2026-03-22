package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

type WebDAVConfig struct {
	Name     string `yaml:"name" json:"name"`
	URL      string `yaml:"url" json:"url"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
	Timeout  int    `yaml:"timeout" json:"timeout"`
}

type ScheduleConfig struct {
	Type   string `yaml:"type" json:"type"`
	Day    int    `yaml:"day" json:"day"`
	Hour   int    `yaml:"hour" json:"hour"`
	Minute int    `yaml:"minute" json:"minute"`
}

type WebServerConfig struct {
	Host     string `yaml:"host" json:"host"`
	Port     int    `yaml:"port" json:"port"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
}

type BackupItem struct {
	Path string `yaml:"path" json:"path"`
}

type LocalBackupTask struct {
	Name       string         `yaml:"name" json:"name"`
	Type       string         `yaml:"type" json:"type"`
	Enabled    bool           `yaml:"enabled" json:"enabled"`
	Paths      []BackupItem   `yaml:"paths" json:"paths"`
	WebDAV     []string       `yaml:"webdav" json:"webdav"`
	Schedule   ScheduleConfig `yaml:"schedule" json:"schedule"`
	EncryptPwd string         `yaml:"encrypt_pwd" json:"encrypt_pwd"`
	BasePath   string         `yaml:"base_path" json:"base_path"`
}

type NodeImageSyncTask struct {
	Name        string          `yaml:"name" json:"name"`
	Type        string          `yaml:"type" json:"type"`
	Enabled     bool            `yaml:"enabled" json:"enabled"`
	SyncMode    string          `yaml:"sync_mode" json:"sync_mode"`
	NodeImage   NodeImageConfig `yaml:"nodeimage" json:"nodeimage"`
	WebDAV      []string        `yaml:"webdav" json:"webdav"`
	Schedule    ScheduleConfig  `yaml:"schedule" json:"schedule"`
	Concurrency int             `yaml:"concurrency" json:"concurrency"`
}

type NodeImageConfig struct {
	APIKey   string `yaml:"api_key" json:"api_key"`
	Cookie   string `yaml:"cookie" json:"cookie"`
	APIURL   string `yaml:"api_url" json:"api_url"`
	BasePath string `yaml:"base_path" json:"base_path"`
}

type Config struct {
	WebDAV         []WebDAVConfig      `yaml:"webdav" json:"webdav"`
	LocalTasks     []LocalBackupTask   `yaml:"local_tasks" json:"local_tasks"`
	NodeImageTasks []NodeImageSyncTask `yaml:"nodeimage_tasks" json:"nodeimage_tasks"`
	WebServer      WebServerConfig     `yaml:"webserver" json:"webserver"`
}

var (
	configPath string
	configMu   sync.RWMutex
)

func SetConfigPath(path string) {
	configPath = path
}

func GetConfigPath() string {
	return configPath
}

func Load(path string) (*Config, error) {
	configMu.Lock()
	defer configMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := defaultConfig()
			if createErr := createConfigDir(path); createErr != nil {
				return nil, fmt.Errorf("failed to create config directory: %w", createErr)
			}
			if saveErr := saveConfigLocked(path, cfg); saveErr != nil {
				return nil, fmt.Errorf("failed to create default config: %w", saveErr)
			}
			return cfg, nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	applyDefaults(&cfg)
	return &cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		WebDAV:         []WebDAVConfig{},
		LocalTasks:     []LocalBackupTask{},
		NodeImageTasks: []NodeImageSyncTask{},
		WebServer: WebServerConfig{
			Host:     "0.0.0.0",
			Port:     8080,
			Username: "admin",
			Password: "admin",
		},
	}
}

func createConfigDir(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0755)
}

func applyDefaults(cfg *Config) {
	for i := range cfg.WebDAV {
		if cfg.WebDAV[i].Timeout == 0 {
			cfg.WebDAV[i].Timeout = 300
		}
	}

	if cfg.WebServer.Host == "" {
		cfg.WebServer.Host = "0.0.0.0"
	}
	if cfg.WebServer.Port == 0 {
		cfg.WebServer.Port = 8080
	}
}

func saveConfigLocked(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func Save(path string, cfg *Config) error {
	configMu.Lock()
	defer configMu.Unlock()
	return saveConfigLocked(path, cfg)
}

func (c *Config) GetWebDAVByName(name string) *WebDAVConfig {
	for i := range c.WebDAV {
		if c.WebDAV[i].Name == name {
			result := c.WebDAV[i]
			return &result
		}
	}
	return nil
}

func (c *Config) GetLocalTaskByName(name string) *LocalBackupTask {
	for i := range c.LocalTasks {
		if c.LocalTasks[i].Name == name {
			result := c.LocalTasks[i]
			return &result
		}
	}
	return nil
}

func (c *Config) GetNodeImageTaskByName(name string) *NodeImageSyncTask {
	for i := range c.NodeImageTasks {
		if c.NodeImageTasks[i].Name == name {
			result := c.NodeImageTasks[i]
			return &result
		}
	}
	return nil
}

func (c *Config) GetTaskByName(name string) interface{} {
	if task := c.GetLocalTaskByName(name); task != nil {
		return task
	}
	if task := c.GetNodeImageTaskByName(name); task != nil {
		return task
	}
	return nil
}

func (c *Config) UpdateLocalTask(name string, task *LocalBackupTask) {
	for i := range c.LocalTasks {
		if c.LocalTasks[i].Name == name {
			c.LocalTasks[i] = *task
			return
		}
	}
	c.LocalTasks = append(c.LocalTasks, *task)
}

func (c *Config) UpdateNodeImageTask(name string, task *NodeImageSyncTask) {
	for i := range c.NodeImageTasks {
		if c.NodeImageTasks[i].Name == name {
			c.NodeImageTasks[i] = *task
			return
		}
	}
	c.NodeImageTasks = append(c.NodeImageTasks, *task)
}

func (c *Config) DeleteLocalTask(name string) {
	for i := range c.LocalTasks {
		if c.LocalTasks[i].Name == name {
			c.LocalTasks = append(c.LocalTasks[:i], c.LocalTasks[i+1:]...)
			return
		}
	}
}

func (c *Config) DeleteNodeImageTask(name string) {
	for i := range c.NodeImageTasks {
		if c.NodeImageTasks[i].Name == name {
			c.NodeImageTasks = append(c.NodeImageTasks[:i], c.NodeImageTasks[i+1:]...)
			return
		}
	}
}

func (c *Config) AddWebDAV(wd *WebDAVConfig) {
	c.WebDAV = append(c.WebDAV, *wd)
}

func (c *Config) DeleteWebDAV(name string) {
	for i := range c.WebDAV {
		if c.WebDAV[i].Name == name {
			c.WebDAV = append(c.WebDAV[:i], c.WebDAV[i+1:]...)
			return
		}
	}
}

func (s *ScheduleConfig) String() string {
	switch s.Type {
	case "hourly":
		return fmt.Sprintf("Hourly at minute %d", s.Minute)
	case "daily":
		return fmt.Sprintf("Daily at %02d:%02d", s.Hour, s.Minute)
	case "weekly":
		days := []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
		dayName := "Sunday"
		if s.Day >= 0 && s.Day < 7 {
			dayName = days[s.Day]
		}
		return fmt.Sprintf("Weekly on %s at %02d:%02d", dayName, s.Hour, s.Minute)
	default:
		return "Unknown schedule"
	}
}
