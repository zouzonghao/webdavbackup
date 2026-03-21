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

type BackupItem struct {
	Path string `yaml:"path" json:"path"`
	Type string `yaml:"type" json:"type"`
}

type ScheduleConfig struct {
	Type   string `yaml:"type" json:"type"`
	Day    int    `yaml:"day" json:"day"`
	Hour   int    `yaml:"hour" json:"hour"`
	Minute int    `yaml:"minute" json:"minute"`
}

type BackupTask struct {
	Name     string         `yaml:"name" json:"name"`
	Enabled  bool           `yaml:"enabled" json:"enabled"`
	Paths    []BackupItem   `yaml:"paths" json:"paths"`
	WebDAV   []string       `yaml:"webdav" json:"webdav"`
	Schedule ScheduleConfig `yaml:"schedule" json:"schedule"`
}

type WebServerConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Port     int    `yaml:"port" json:"port"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
}

type Config struct {
	WebDAV    []WebDAVConfig  `yaml:"webdav" json:"webdav"`
	Tasks     []BackupTask    `yaml:"tasks" json:"tasks"`
	WebServer WebServerConfig `yaml:"webserver" json:"webserver"`
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
	configMu.RLock()
	defer configMu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := defaultConfig()
			if createErr := createConfigDir(path); createErr != nil {
				return nil, fmt.Errorf("failed to create config directory: %w", createErr)
			}
			if saveErr := saveConfig(path, cfg); saveErr != nil {
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
		WebDAV: []WebDAVConfig{},
		Tasks:  []BackupTask{},
		WebServer: WebServerConfig{
			Enabled:  true,
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

	if cfg.WebServer.Port == 0 {
		cfg.WebServer.Port = 8080
	}
}

func saveConfig(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func Save(path string, cfg *Config) error {
	configMu.Lock()
	defer configMu.Unlock()

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (c *Config) GetWebDAVByName(name string) *WebDAVConfig {
	for i := range c.WebDAV {
		if c.WebDAV[i].Name == name {
			return &c.WebDAV[i]
		}
	}
	return nil
}

func (c *Config) GetTaskByName(name string) *BackupTask {
	for i := range c.Tasks {
		if c.Tasks[i].Name == name {
			return &c.Tasks[i]
		}
	}
	return nil
}

func (c *Config) UpdateTask(name string, task *BackupTask) {
	for i := range c.Tasks {
		if c.Tasks[i].Name == name {
			c.Tasks[i] = *task
			return
		}
	}
	c.Tasks = append(c.Tasks, *task)
}

func (c *Config) DeleteTask(name string) {
	for i := range c.Tasks {
		if c.Tasks[i].Name == name {
			c.Tasks = append(c.Tasks[:i], c.Tasks[i+1:]...)
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
