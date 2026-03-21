package webdav

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
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

func (c *Client) UploadStream(reader io.Reader, size int64, remotePath string) error {
	remoteURL := fmt.Sprintf("%s/%s", c.URL, strings.TrimPrefix(remotePath, "/"))

	logger.Info("[%s] Starting upload: %s (%.2f MB)", c.Name, remotePath, float64(size)/1024/1024)

	var lastErr error
	for retry := 0; retry < 3; retry++ {
		if retry > 0 {
			logger.Warn("[%s] Retry %d/3 for %s", c.Name, retry, remotePath)
			time.Sleep(time.Duration(retry) * time.Second)
		}

		pr := &progressReader{
			reader: reader,
			total:  size,
			name:   c.Name,
		}

		req, err := http.NewRequest("PUT", remoteURL, pr)
		if err != nil {
			lastErr = fmt.Errorf("failed to create request: %w", err)
			continue
		}

		req.SetBasicAuth(c.Username, c.Password)
		req.ContentLength = size

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
