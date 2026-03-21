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

	pr := &progressReader{
		reader: reader,
		total:  size,
		name:   c.Name,
	}

	req, err := http.NewRequest("PUT", remoteURL, pr)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(c.Username, c.Password)
	req.ContentLength = -1

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		logger.Info("[%s] Upload completed: %s", c.Name, remotePath)
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(body))
}

func (c *Client) TestConnection() error {
	testClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
			},
		},
	}

	req, err := http.NewRequest("PROPFIND", c.URL, bytes.NewReader([]byte{}))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(c.Username, c.Password)
	req.Header.Set("Depth", "0")

	resp, err := testClient.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("authentication failed")
	case http.StatusNotFound:
		return fmt.Errorf("path not found")
	case http.StatusForbidden:
		return fmt.Errorf("access forbidden")
	case http.StatusMethodNotAllowed:
		return fmt.Errorf("not a WebDAV endpoint")
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("server error: %d", resp.StatusCode)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("client error: %d", resp.StatusCode)
	}

	return nil
}
