package webdav

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"webdav-backup/logger"
)

type EnhancedConfig struct {
	Name     string
	URL      string
	Username string
	Password string
	Timeout  int
}

type Config struct {
	Name     string
	URL      string
	Username string
	Password string
	Timeout  int
}

type EnhancedClient struct {
	Name       string
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

type Client struct {
	Name     string
	enhanced *EnhancedClient
}

type FileInfo struct {
	Path string
	Size int64
}

type propfindResponse struct {
	XMLName   xml.Name   `xml:"multistatus"`
	Responses []response `xml:"response"`
}

type response struct {
	Href     string   `xml:"href"`
	Propstat propstat `xml:"propstat"`
}

type propstat struct {
	Status string `xml:"status"`
	Prop   prop   `xml:"prop"`
}

type prop struct {
	DisplayName      string `xml:"displayname"`
	GetContentLength string `xml:"getcontentlength"`
	LastModified     string `xml:"getlastmodified"`
	ContentType      string `xml:"getcontenttype"`
}

func NewEnhancedClient(cfg EnhancedConfig) *EnhancedClient {
	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout == 0 {
		timeout = 300 * time.Second
	}

	return &EnhancedClient{
		Name:     cfg.Name,
		baseURL:  strings.TrimSuffix(cfg.URL, "/"),
		username: cfg.Username,
		password: cfg.Password,
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: false,
				},
			},
		},
	}
}

func NewClient(cfg Config) *Client {
	enhanced := NewEnhancedClient(EnhancedConfig{
		Name:     cfg.Name,
		URL:      cfg.URL,
		Username: cfg.Username,
		Password: cfg.Password,
		Timeout:  cfg.Timeout,
	})
	return &Client{
		Name:     cfg.Name,
		enhanced: enhanced,
	}
}

func (c *Client) UploadStream(reader io.Reader, size int64, remotePath string) error {
	return c.enhanced.UploadStream(reader, size, remotePath)
}

func (c *Client) TestConnection() error {
	return c.enhanced.TestConnection()
}

func (c *EnhancedClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	url := fmt.Sprintf("%s/%s", c.baseURL, strings.TrimPrefix(path, "/"))
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	return req, nil
}

func (c *EnhancedClient) do(req *http.Request) (*http.Response, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP错误 %d: %s", resp.StatusCode, string(body))
	}

	return resp, nil
}

func (c *EnhancedClient) UploadStream(reader io.Reader, size int64, remotePath string) error {
	req, err := c.newRequest(context.Background(), "PUT", remotePath, reader)
	if err != nil {
		return fmt.Errorf("创建上传请求失败: %w", err)
	}

	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("上传失败: %w", err)
	}
	defer resp.Body.Close()

	logger.Info("[%s] 上传完成: %s (%.2f MB)", c.Name, remotePath, float64(size)/1024/1024)
	return nil
}

func (c *EnhancedClient) ListFiles(path string) ([]FileInfo, error) {
	ctx := context.Background()

	propfindBody := `<?xml version="1.0" encoding="utf-8"?>
<propfind xmlns="DAV:">
  <prop>
    <displayname/>
    <getcontentlength/>
    <getlastmodified/>
    <getcontenttype/>
  </prop>
</propfind>`

	req, err := c.newRequest(ctx, "PROPFIND", path, bytes.NewReader([]byte(propfindBody)))
	if err != nil {
		return nil, fmt.Errorf("创建文件列表请求失败: %w", err)
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var response propfindResponse
	if err := xml.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("解析XML响应失败: %w", err)
	}

	var files []FileInfo
	for _, r := range response.Responses {
		decodedHref, _ := url.QueryUnescape(r.Href)
		if decodedHref == path || decodedHref == path+"/" || strings.HasSuffix(decodedHref, "/") && decodedHref == strings.TrimSuffix(path, "/")+"/" {
			continue
		}

		if r.Propstat.Prop.GetContentLength == "" {
			continue
		}

		if strings.HasSuffix(decodedHref, "/") {
			continue
		}

		filename := filepath.Base(decodedHref)
		size, _ := strconv.ParseInt(r.Propstat.Prop.GetContentLength, 10, 64)

		files = append(files, FileInfo{
			Path: path + "/" + filename,
			Size: size,
		})
	}

	return files, nil
}

func (c *EnhancedClient) Mkdir(path string) error {
	ctx := context.Background()
	req, err := c.newRequest(ctx, "MKCOL", path, nil)
	if err != nil {
		return fmt.Errorf("创建目录请求失败: %w", err)
	}

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
		logger.Info("[%s] 目录创建成功: %s", c.Name, path)
		return nil
	case http.StatusOK:
		// 某些 WebDAV 服务器在目录已存在时返回 200
		return nil
	case http.StatusMethodNotAllowed:
		// 405 表示目录已存在
		return nil
	default:
		return fmt.Errorf("创建目录失败，状态码: %d", resp.StatusCode)
	}
}

func (c *EnhancedClient) Delete(path string) error {
	ctx := context.Background()
	req, err := c.newRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("创建删除请求失败: %w", err)
	}

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		logger.Info("[%s] 删除成功: %s", c.Name, path)
		return nil
	}

	return fmt.Errorf("删除失败，状态码: %d", resp.StatusCode)
}

func (c *EnhancedClient) TestConnection() error {
	ctx := context.Background()

	req, err := c.newRequest(ctx, "PROPFIND", "", bytes.NewReader([]byte{}))
	if err != nil {
		return fmt.Errorf("创建测试请求失败: %w", err)
	}
	req.Header.Set("Depth", "0")

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("认证失败")
	case http.StatusNotFound:
		return fmt.Errorf("路径不存在")
	case http.StatusForbidden:
		return fmt.Errorf("访问被拒绝")
	case http.StatusMethodNotAllowed:
		return fmt.Errorf("不是WebDAV端点")
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		logger.Info("[%s] 连接测试成功", c.Name)
		return nil
	}

	return fmt.Errorf("连接测试失败，状态码: %d", resp.StatusCode)
}

func (c *EnhancedClient) EnsureDirectory(path string) error {
	files, err := c.ListFiles(path)
	if err == nil && len(files) >= 0 {
		return nil
	}

	logger.Info("[%s] 创建目录: %s", c.Name, path)
	return c.Mkdir(path)
}

func (c *EnhancedClient) Upload(reader io.Reader, size int64, remotePath string) error {
	return c.UploadStream(reader, size, remotePath)
}

func (c *EnhancedClient) GetName() string {
	return c.Name
}
