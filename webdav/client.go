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
	"path"
	"path/filepath"
	"regexp"
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
	Href      string     `xml:"href"`
	Propstats []propstat `xml:"propstat"` // 支持多个 propstat 元素
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

// linkNextRegex 用于从 Link 响应头中提取下一页的 URL（支持坚果云分页）
var linkNextRegex = regexp.MustCompile(`<(.+?)>; rel="next"`)

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

func (c *EnhancedClient) newRequest(ctx context.Context, method, requestPath string, body io.Reader) (*http.Request, error) {
	// 智能处理相对路径和绝对 URL（用于分页）
	parsedPath, err := url.Parse(requestPath)
	if err != nil {
		return nil, fmt.Errorf("无法解析路径 '%s': %w", requestPath, err)
	}

	var targetURL string
	// 如果是完整的 URL（例如，来自 Link 头），则直接使用
	if parsedPath.IsAbs() {
		targetURL = parsedPath.String()
	} else {
		// 否则，将其与 baseURL 拼接
		targetURL = fmt.Sprintf("%s/%s", c.baseURL, strings.TrimPrefix(requestPath, "/"))
	}

	// 对于 PROPFIND 请求，如果是目录路径（不以文件扩展名结尾），确保 URL 以斜杠结尾
	// 这是 Apache WebDAV 的要求
	if method == "PROPFIND" && !strings.Contains(path.Base(targetURL), ".") {
		if !strings.HasSuffix(targetURL, "/") {
			targetURL += "/"
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
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

func (c *EnhancedClient) ListFiles(requestPath string) ([]FileInfo, error) {
	var allFiles []FileInfo
	nextPagePath := requestPath // 初始路径用于第一个请求

	// 循环处理分页
	for {
		files, nextPage, err := c.listFilesPage(nextPagePath, requestPath)
		if err != nil {
			return nil, err
		}
		allFiles = append(allFiles, files...)

		// 检查是否有下一页
		if nextPage == "" {
			break
		}
		nextPagePath = nextPage
		logger.Info("[%s] 检测到分页，继续获取下一页: %s", c.Name, nextPage)
	}

	return allFiles, nil
}

// listFilesPage 处理单页 PROPFIND 请求，返回文件列表和下一页 URL
func (c *EnhancedClient) listFilesPage(requestPath, basePath string) ([]FileInfo, string, error) {
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

	req, err := c.newRequest(ctx, "PROPFIND", requestPath, bytes.NewReader([]byte(propfindBody)))
	if err != nil {
		return nil, "", fmt.Errorf("创建文件列表请求失败: %w", err)
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	// 记录请求信息用于调试
	logger.Debug("[%s] PROPFIND 请求: %s", c.Name, req.URL.String())

	resp, err := c.do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	// 记录响应状态和头信息
	logger.Debug("[%s] 响应状态: %s, Content-Type: %s", c.Name, resp.Status, resp.Header.Get("Content-Type"))

	// 限制读取的响应体大小（防止内存问题）
	// 最大 100MB，对于大多数文件列表应该足够
	maxBodySize := int64(100 * 1024 * 1024)
	limitedReader := io.LimitReader(resp.Body, maxBodySize+1)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, "", fmt.Errorf("读取响应体失败: %w", err)
	}
	if int64(len(bodyBytes)) > maxBodySize {
		return nil, "", fmt.Errorf("响应体过大（超过 %d MB），可能存在服务器问题", maxBodySize/1024/1024)
	}

	// 检查响应类型
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "xml") && !strings.Contains(contentType, "text/xml") && !strings.Contains(contentType, "application/xml") {
		// 显示错误提示
		preview := string(bodyBytes)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		
		// 提供更明确的错误提示
		var hint string
		if resp.StatusCode == http.StatusOK && strings.Contains(contentType, "html") {
			hint = "服务器返回了 HTML 目录索引而不是 WebDAV 响应，可能是：\n" +
				"  1. 认证失败（用户名或密码错误）\n" +
				"  2. WebDAV 未启用或配置错误\n" +
				"  3. URL 错误（应使用 WebDAV 端点而非普通 HTTP）\n" +
				"  建议使用 curl 测试：curl -X PROPFIND -u '用户名:密码' 'URL'"
		} else {
			hint = "可能不支持WebDAV协议或认证失败"
		}
		
		return nil, "", fmt.Errorf("服务器返回非XML响应 (状态码: %d, Content-Type: %s)\n%s\n响应预览: %s", 
			resp.StatusCode, contentType, hint, preview)
	}

	var response propfindResponse
	if err := xml.Unmarshal(bodyBytes, &response); err != nil {
		// 尝试保存响应到临时文件以便调试
		logger.Error("解析XML失败，响应内容: %s", string(bodyBytes))
		return nil, "", fmt.Errorf("解析XML响应失败: %w", err)
	}

	if len(response.Responses) == 0 {
		logger.Warn("未解析到任何文件响应")
	} else {
		logger.Info("解析到 %d 个响应", len(response.Responses))
	}

	var files []FileInfo
	for _, r := range response.Responses {
		// 使用 PathUnescape 而不是 QueryUnescape（WebDAV Href 是 URL 路径）
		decodedHref, err := url.PathUnescape(r.Href)
		if err != nil {
			logger.Warn("URL 解码失败: %v, 原始 Href: %s", err, r.Href)
			continue
		}

		// 跳过请求的目录本身
		if decodedHref == requestPath || decodedHref == requestPath+"/" || strings.HasSuffix(decodedHref, "/") && decodedHref == strings.TrimSuffix(requestPath, "/")+"/" {
			continue
		}

		// 跳过子目录
		if strings.HasSuffix(decodedHref, "/") {
			continue
		}

		// 查找成功的 propstat（status = 200 OK）
		var successPropstat *propstat
		for _, ps := range r.Propstats {
			if strings.Contains(ps.Status, "200") {
				successPropstat = &ps
				break
			}
		}

		if successPropstat == nil {
			logger.Warn("文件 %s 没有成功的 propstat，跳过", decodedHref)
			continue
		}

		if successPropstat.Prop.GetContentLength == "" {
			logger.Warn("文件 %s 缺少 contentlength 属性，可能是特殊文件", decodedHref)
			continue
		}

		filename := filepath.Base(decodedHref)
		size, _ := strconv.ParseInt(successPropstat.Prop.GetContentLength, 10, 64)

		files = append(files, FileInfo{
			Path: path.Join(basePath, filename), // 使用 path.Join 规范化路径
			Size: size,
		})
	}

	// 检查 Link 响应头以处理分页（支持坚果云）
	linkHeader := resp.Header.Get("Link")
	matches := linkNextRegex.FindStringSubmatch(linkHeader)
	var nextPage string
	if len(matches) > 1 {
		// Link 头提供的是完整的 URL，直接用于下一次请求
		nextPage = matches[1]
		logger.Info("[%s] 发现下一页链接: %s", c.Name, nextPage)
	}

	return files, nextPage, nil
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
