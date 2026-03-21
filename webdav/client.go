package webdav

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"webdav-backup/logger"
)

type Client struct {
	Name     string
	URL      string
	Username string
	Password string
	Timeout  time.Duration
	client   *http.Client
}

type Config struct {
	Name     string
	URL      string
	Username string
	Password string
	Timeout  int
}

type progressReader struct {
	reader  io.Reader
	total   int64
	read    int64
	lastLog int64
	name    string
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.read += int64(n)

	if pr.total > 0 {
		progress := float64(pr.read) / float64(pr.total) * 100
		if pr.read-pr.lastLog > pr.total/20 || pr.read == pr.total {
			logger.Info("[%s] Upload progress: %.1f%% (%.2f MB / %.2f MB)",
				pr.name, progress,
				float64(pr.read)/1024/1024,
				float64(pr.total)/1024/1024)
			pr.lastLog = pr.read
		}
	}

	return n, err
}

func NewClient(cfg Config) *Client {
	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout == 0 {
		timeout = 300 * time.Second
	}

	return &Client{
		Name:     cfg.Name,
		URL:      strings.TrimSuffix(cfg.URL, "/"),
		Username: cfg.Username,
		Password: cfg.Password,
		Timeout:  timeout,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: false,
				},
			},
		},
	}
}

func (c *Client) Upload(localPath, remotePath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	remoteURL := fmt.Sprintf("%s/%s", c.URL, strings.TrimPrefix(remotePath, "/"))

	logger.Info("[%s] Starting upload: %s (%.2f MB)", c.Name, remotePath, float64(stat.Size())/1024/1024)

	var lastErr error
	for retry := 0; retry < 3; retry++ {
		if retry > 0 {
			logger.Warn("[%s] Retry %d/3 for %s", c.Name, retry, localPath)
			time.Sleep(time.Duration(retry) * time.Second)
			file.Seek(0, 0)
		}

		pr := &progressReader{
			reader: file,
			total:  stat.Size(),
			name:   c.Name,
		}

		req, err := http.NewRequest("PUT", remoteURL, pr)
		if err != nil {
			lastErr = fmt.Errorf("failed to create request: %w", err)
			continue
		}

		req.SetBasicAuth(c.Username, c.Password)
		req.ContentLength = stat.Size()

		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			logger.Info("[%s] Upload completed: %s", c.Name, remotePath)
			return nil
		}

		body, _ := io.ReadAll(resp.Body)
		lastErr = fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	return lastErr
}

func (c *Client) Download(remotePath, localPath string) error {
	remoteURL := fmt.Sprintf("%s/%s", c.URL, strings.TrimPrefix(remotePath, "/"))

	req, err := http.NewRequest("GET", remoteURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(c.Username, c.Password)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	return err
}

func (c *Client) Delete(remotePath string) error {
	remoteURL := fmt.Sprintf("%s/%s", c.URL, strings.TrimPrefix(remotePath, "/"))

	req, err := http.NewRequest("DELETE", remoteURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(c.Username, c.Password)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("delete failed with status %d: %s", resp.StatusCode, string(body))
}

func (c *Client) List(remoteDir string) ([]string, error) {
	remoteURL := fmt.Sprintf("%s/%s", c.URL, strings.TrimPrefix(remoteDir, "/"))

	req, err := http.NewRequest("PROPFIND", remoteURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(c.Username, c.Password)
	req.Header.Set("Depth", "1")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return parseWebDAVResponse(body, remoteDir), nil
}

func (c *Client) CreateDir(remotePath string) error {
	remotePath = strings.TrimPrefix(remotePath, "/")
	remotePath = strings.TrimSuffix(remotePath, "/")

	parts := strings.Split(remotePath, "/")
	currentPath := ""

	for _, part := range parts {
		if part == "" {
			continue
		}
		currentPath += "/" + part
		remoteURL := fmt.Sprintf("%s%s", c.URL, currentPath)

		req, err := http.NewRequest("MKCOL", remoteURL, nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		req.SetBasicAuth(c.Username, c.Password)

		resp, err := c.client.Do(req)
		if err != nil {
			return fmt.Errorf("request failed: %w", err)
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			continue
		}

		if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusConflict {
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create directory %s failed with status %d: %s", currentPath, resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) TestConnection() error {
	req, err := http.NewRequest("PROPFIND", c.URL, bytes.NewReader([]byte{}))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(c.Username, c.Password)
	req.Header.Set("Depth", "0")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("authentication failed")
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("server error: %d", resp.StatusCode)
	}

	return nil
}

func parseWebDAVResponse(body []byte, remoteDir string) []string {
	var files []string
	lines := strings.Split(string(body), "\n")

	for _, line := range lines {
		if strings.Contains(line, "<d:href>") || strings.Contains(line, "<href>") {
			start := strings.Index(line, ">")
			end := strings.LastIndex(line, "<")
			if start != -1 && end != -1 && end > start {
				href := line[start+1 : end]
				href, _ = url.PathUnescape(href)
				name := filepath.Base(href)
				if name != "" && name != filepath.Base(remoteDir) {
					files = append(files, name)
				}
			}
		}
	}

	return files
}
