package engine

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mzky/zip"

	"webdav-backup/config"
	"webdav-backup/logger"
	"webdav-backup/nodeimage"
	"webdav-backup/webdav"
)

func joinWebDAVPath(base, sub string) string {
	base = strings.TrimRight(base, "/")
	sub = strings.TrimLeft(sub, "/")
	if sub == "" {
		return base
	}
	return base + "/" + sub
}

const (
	cacheMaxItems = 10000 // 缓存最大文件数
)

var (
	webdavCache   []webdav.FileInfo
	webdavCacheMu sync.RWMutex
)

func InvalidateWebdavCache() {
	webdavCacheMu.Lock()
	defer webdavCacheMu.Unlock()
	webdavCache = nil
}

// setCacheWithLimit 设置缓存，如果超过限制则不缓存
func setCacheWithLimit(files []webdav.FileInfo) {
	if len(files) > cacheMaxItems {
		logger.Warn("[WebDAV Cache] 文件数量 %d 超过限制 %d，跳过缓存", len(files), cacheMaxItems)
		return
	}
	webdavCache = files
}

type Executor struct {
	config *config.Config
}

func NewExecutor(cfg *config.Config) *Executor {
	return &Executor{
		config: cfg,
	}
}

type memTracker struct {
	maxAlloc uint64
	maxSys   uint64
}

func (m *memTracker) update() {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	if stats.Alloc > m.maxAlloc {
		m.maxAlloc = stats.Alloc
	}
	if stats.Sys > m.maxSys {
		m.maxSys = stats.Sys
	}
}

type StreamResult struct {
	Stream    io.ReadCloser
	TotalSize int64
	FileCount int
	SizeChan  chan int64
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.writer.Write(p)
	cw.count += int64(n)
	return n, err
}

type fileEntry struct {
	path  string
	info  os.FileInfo
	isDir bool
}

func (e *Executor) ExecuteLocalTask(task *config.LocalBackupTask, webdavClients map[string]*webdav.EnhancedClient) error {
	logger.Info("[%s] ========== 开始本地备份任务 ==========", task.Name)

	mem := &memTracker{}
	mem.update()

	if len(task.Paths) == 0 {
		return fmt.Errorf("未配置备份路径")
	}

	logger.Info("[%s] 验证备份路径...", task.Name)
	var validPaths []config.BackupItem
	for _, item := range task.Paths {
		// 检查每一级目录
		pathParts := strings.Split(item.Path, "/")
		currentPath := ""
		for i, part := range pathParts {
			if part == "" {
				continue
			}
			currentPath += "/" + part
			if _, err := os.Stat(currentPath); err != nil {
				logger.Warn("[%s] 路径检查失败在第 %d 级: %s (%v)", task.Name, i, currentPath, err)
				break
			}
		}
		
		info, err := os.Stat(item.Path)
		if err != nil {
			logger.Warn("[%s] 路径不可访问，跳过: %s (%v)", task.Name, item.Path, err)
			// 尝试检查文件是否可读
			if file, openErr := os.Open(item.Path); openErr != nil {
				logger.Warn("[%s] 文件打开失败: %v", task.Name, openErr)
			} else {
				file.Close()
				logger.Warn("[%s] 文件可以打开但 stat 失败", task.Name)
			}
			continue
		}
		validPaths = append(validPaths, config.BackupItem{
			Path:         item.Path,
			ExcludePaths: item.ExcludePaths,
		})
		if info.IsDir() {
			logger.Info("[%s] 路径验证通过: %s (目录)", task.Name, item.Path)
		} else {
			logger.Info("[%s] 路径验证通过: %s (文件)", task.Name, item.Path)
		}
	}

	if len(validPaths) == 0 {
		return fmt.Errorf("没有有效的备份路径")
	}

	taskCopy := *task
	taskCopy.Paths = validPaths

	timestamp := time.Now().Format("20060102_150405")
	zipFileName := fmt.Sprintf("%s_%s.zip", taskCopy.Name, timestamp)

	var uploadErrors []string
	for _, webdavName := range taskCopy.WebDAV {
		var client *webdav.EnhancedClient
		var exists bool

		if webdavClients != nil {
			client, exists = webdavClients[webdavName]
		}

		if !exists {
			if e.config == nil {
				uploadErrors = append(uploadErrors, fmt.Sprintf("%s: 配置未加载", webdavName))
				continue
			}
			wdCfg := e.config.GetWebDAVByName(webdavName)
			if wdCfg == nil {
				uploadErrors = append(uploadErrors, fmt.Sprintf("%s: 配置中不存在", webdavName))
				continue
			}
			client = webdav.NewEnhancedClient(webdav.EnhancedConfig{
				Name:     wdCfg.Name,
				URL:      wdCfg.URL,
				Username: wdCfg.Username,
				Password: wdCfg.Password,
				Timeout:  wdCfg.Timeout,
			})
		}

		logger.Info("[%s] 测试 WebDAV 服务器连接: %s", taskCopy.Name, webdavName)
		if err := client.TestConnection(); err != nil {
			uploadErrors = append(uploadErrors, fmt.Sprintf("%s: 连接失败: %v", webdavName, err))
			continue
		}

		remotePath := zipFileName
		if taskCopy.BasePath != "" {
			remotePath = joinWebDAVPath(taskCopy.BasePath, zipFileName)
			if err := client.EnsureDirectory(taskCopy.BasePath); err != nil {
				uploadErrors = append(uploadErrors, fmt.Sprintf("%s: 创建目录失败: %v", webdavName, err))
				continue
			}
		}

		mem.update()
		result, err := e.createStream(&taskCopy)
		if err != nil {
			uploadErrors = append(uploadErrors, fmt.Sprintf("%s: 创建压缩流失败: %v", webdavName, err))
			continue
		}

		mem.update()
		logger.Info("[%s] 上传到 %s，文件名: %s (源文件: %.2f MB)", taskCopy.Name, webdavName, remotePath, float64(result.TotalSize)/1024/1024)

		if err := client.UploadStream(result.Stream, result.TotalSize, remotePath); err != nil {
			result.Stream.Close()
			uploadErrors = append(uploadErrors, fmt.Sprintf("%s: 上传失败: %v", webdavName, err))
			continue
		}
		result.Stream.Close()

		zipSize := <-result.SizeChan
		mem.update()
		logger.Info("[%s] 备份已上传到 %s - 源文件: %.2f MB, 压缩后: %.2f MB (%.1f%%)",
			taskCopy.Name, webdavName,
			float64(result.TotalSize)/1024/1024,
			float64(zipSize)/1024/1024,
			float64(zipSize)/float64(result.TotalSize)*100)
	}

	if len(uploadErrors) > 0 {
		return fmt.Errorf("上传错误: %v", uploadErrors)
	}

	mem.update()
	logger.Info("[%s] 内存峰值 - Alloc: %.2f MB, Sys: %.2f MB",
		task.Name,
		float64(mem.maxAlloc)/1024/1024,
		float64(mem.maxSys)/1024/1024)
	logger.Info("[%s] ========== 本地备份任务完成 ==========", taskCopy.Name)
	return nil
}

func (e *Executor) createStream(task *config.LocalBackupTask) (*StreamResult, error) {
	var files []fileEntry
	var totalSize int64

	for _, item := range task.Paths {
		excludePaths := item.ExcludePaths
		err := filepath.Walk(item.Path, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
		// 检查是否在排除路径中
		for _, excludePath := range excludePaths {
			// 精确匹配或子路径匹配（防止 /opt/data 匹配到 /opt/data2）
			if path == excludePath || strings.HasPrefix(path, excludePath+string(os.PathSeparator)) {
				if info.IsDir() {
					logger.Info("[%s] 跳过排除目录: %s", task.Name, path)
					return filepath.SkipDir
				}
				return nil
			}
		}
			files = append(files, fileEntry{
				path:  path,
				info:  info,
				isDir: info.IsDir(),
			})
			if !info.IsDir() {
				totalSize += info.Size()
			}
			return nil
		})
		if err != nil {
			logger.Warn("[%s] 遍历路径失败 %s: %v", task.Name, item.Path, err)
		}
	}

	fileCount := 0
	for _, f := range files {
		if !f.isDir {
			fileCount++
		}
	}

	logger.Info("[%s] 发现 %d 个文件，总大小: %.2f MB", task.Name, fileCount, float64(totalSize)/1024/1024)

	pr, pw := io.Pipe()
	cw := &countingWriter{writer: pw}
	sizeChan := make(chan int64, 1)

	go func() {
		defer pw.Close()

		zipWriter := zip.NewWriter(cw)
		defer zipWriter.Close()

		processed := 0
		for _, f := range files {
			if f.isDir {
				_, err := zipWriter.Create(f.path + "/")
				if err != nil {
					logger.Warn("[%s] 添加目录失败 %s: %v", task.Name, f.path, err)
				}
			} else {
				if err := e.addFileToZip(zipWriter, f.path, f.info, &processed, fileCount, task.Name, task.EncryptPwd); err != nil {
					logger.Warn("[%s] 添加文件失败 %s: %v", task.Name, f.path, err)
				}
			}
		}

		logger.Info("[%s] 压缩完成: %d 个文件", task.Name, processed)
		sizeChan <- cw.count
	}()

	return &StreamResult{
		Stream:    pr,
		TotalSize: totalSize,
		FileCount: fileCount,
		SizeChan:  sizeChan,
	}, nil
}

func (e *Executor) addFileToZip(zipWriter *zip.Writer, filePath string, info os.FileInfo, processed *int, total int, taskName string, password string) error {
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("创建文件头失败: %w", err)
	}
	header.Name = filePath
	header.Method = zip.Deflate

	var writer io.Writer
	if password != "" {
		writer, err = zipWriter.Encrypt(header.Name, password, zip.AES256Encryption)
		if err != nil {
			return fmt.Errorf("创建加密条目失败: %w", err)
		}
	} else {
		writer, err = zipWriter.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("创建条目失败: %w", err)
		}
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer file.Close()

	_, err = io.Copy(writer, file)
	if err != nil {
		return fmt.Errorf("复制文件内容失败: %w", err)
	}

	*processed++
	if *processed%100 == 0 || *processed == total {
		logger.Info("[%s] 进度: %d/%d 文件 (%.1f%%)", taskName, *processed, total, float64(*processed)/float64(total)*100)
	}
	return nil
}

func (e *Executor) ExecuteNodeImageTask(task *config.NodeImageSyncTask, webdavClients map[string]*webdav.EnhancedClient) error {
	syncMode := task.SyncMode
	if syncMode == "" {
		syncMode = "incremental"
	}

	isFullSync := syncMode == "full"

	logger.Info("[NodeImage] 开始执行任务 '%s'", task.Name)
	if isFullSync {
		logger.Info("  -> 同步模式: 全量同步 (Cookie)")
		if task.NodeImage.Cookie == "" {
			return fmt.Errorf("全量同步需要配置 Cookie")
		}
	} else {
		logger.Info("  -> 同步模式: 增量同步 (API Key)")
		if task.NodeImage.APIKey == "" {
			return fmt.Errorf("增量同步需要配置 API Key")
		}
	}
	logger.Info("  -> 目标服务器: %v", task.WebDAV)
	logger.Info("  -> 调度: %s", task.Schedule.String())

	apiURL := task.NodeImage.APIURL
	if apiURL == "" {
		apiURL = "https://api.nodeimage.com/api/images"
	}

	client := nodeimage.NewClient(
		task.NodeImage.Cookie,
		task.NodeImage.APIKey,
		apiURL,
	)

	logger.Info("[NodeImage] 获取图片列表...")
	var images []nodeimage.ImageInfo
	var err error

	if isFullSync {
		logger.Info("[NodeImage] 测试连接...")
		if err := client.TestConnection(); err != nil {
			return fmt.Errorf("NodeImage连接测试失败: %w", err)
		}
		logger.Info("[NodeImage] 连接测试成功")
		images, err = client.GetImageListCookie()
	} else {
		images, err = client.GetImageListAPIKey()
	}

	if err != nil {
		return fmt.Errorf("获取图片列表失败: %w", err)
	}
	logger.Info("[NodeImage] 获取到 %d 张图片", len(images))

	for _, webdavName := range task.WebDAV {
		var wdClient *webdav.EnhancedClient
		var exists bool

		if webdavClients != nil {
			wdClient, exists = webdavClients[webdavName]
		}

		if !exists {
			if e.config == nil {
				logger.Error("[NodeImage] WebDAV服务器 '%s' 不存在且配置未加载", webdavName)
				continue
			}
			wdCfg := e.config.GetWebDAVByName(webdavName)
			if wdCfg == nil {
				logger.Error("[NodeImage] WebDAV服务器 '%s' 不存在", webdavName)
				continue
			}
			wdClient = webdav.NewEnhancedClient(webdav.EnhancedConfig{
				Name:     wdCfg.Name,
				URL:      wdCfg.URL,
				Username: wdCfg.Username,
				Password: wdCfg.Password,
				Timeout:  wdCfg.Timeout,
			})
		}

		if err := e.syncToWebDAV(wdClient, images, task, isFullSync); err != nil {
			logger.Error("[NodeImage->%s] 同步失败: %v", webdavName, err)
		} else {
			logger.Info("[NodeImage->%s] 同步完成", webdavName)
		}
	}

	return nil
}

func (e *Executor) syncToWebDAV(client *webdav.EnhancedClient, images []nodeimage.ImageInfo, task *config.NodeImageSyncTask, isFullSync bool) error {
	logger.Info("[NodeImage->%s] 开始同步到WebDAV", client.GetName())

	basePath := task.NodeImage.BasePath
	if basePath == "" {
		basePath = "/nodeimage"
	}

	if err := client.EnsureDirectory(basePath); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	if isFullSync {
		InvalidateWebdavCache()
	}

	webdavCacheMu.RLock()
	cachedFiles := webdavCache
	webdavCacheMu.RUnlock()

	var existingFiles []webdav.FileInfo
	if cachedFiles != nil {
		existingFiles = cachedFiles
		logger.Info("[NodeImage->%s] 从缓存加载 %d 个文件", client.GetName(), len(existingFiles))
	} else {
		var err error
		existingFiles, err = client.ListFiles(basePath)
		if err != nil {
			logger.Warn("[NodeImage->%s] 获取现有文件列表失败: %v，将上传所有图片", client.GetName(), err)
			existingFiles = []webdav.FileInfo{}
		} else {
			webdavCacheMu.Lock()
			setCacheWithLimit(existingFiles)
			webdavCacheMu.Unlock()
			logger.Info("[NodeImage->%s] 发现 %d 个文件", client.GetName(), len(existingFiles))
		}
	}

	existingFileMap := make(map[string]string)
	for _, file := range existingFiles {
		existingFileMap[filepath.Base(file.Path)] = file.Path
	}

	var filesToUpload []nodeimage.ImageInfo
	var filesToDelete []string

	for _, img := range images {
		if _, exists := existingFileMap[img.Filename]; !exists {
			filesToUpload = append(filesToUpload, img)
		}
		delete(existingFileMap, img.Filename)
	}

	if isFullSync {
		for _, path := range existingFileMap {
			filesToDelete = append(filesToDelete, path)
		}
	}

	if len(filesToUpload) == 0 && len(filesToDelete) == 0 {
		logger.Info("[NodeImage->%s] 文件已是最新状态，无需操作", client.GetName())
		return nil
	}

	logger.Info("[NodeImage->%s] 计划上传: %d 张图片", client.GetName(), len(filesToUpload))
	if isFullSync && len(filesToDelete) > 0 {
		logger.Info("[NodeImage->%s] 计划删除: %d 个文件", client.GetName(), len(filesToDelete))
	}

	var wg sync.WaitGroup
	concurrency := task.Concurrency
	if concurrency <= 0 {
		concurrency = 5
	}

	semaphore := make(chan struct{}, concurrency)
	var uploadCount, deleteCount int
	var uploadErrCount, deleteErrCount int
	var lastProgress int
	var mu sync.Mutex

	for _, img := range filesToUpload {
		wg.Add(1)
		go func(img nodeimage.ImageInfo) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			remotePath := fmt.Sprintf("%s/%s", basePath, img.Filename)
			if err := e.uploadImage(client, img, remotePath); err != nil {
				logger.Error("[NodeImage->%s] 上传失败 %s: %v", client.GetName(), img.Filename, err)
				mu.Lock()
				uploadErrCount++
				mu.Unlock()
			} else {
				mu.Lock()
				uploadCount++
				progress := uploadCount * 100 / len(filesToUpload)
				progress = progress / 10 * 10
				if progress > lastProgress {
					logger.Info("[NodeImage->%s] 上传进度: %d/%d (%d%%)", client.GetName(), uploadCount, len(filesToUpload), progress)
					lastProgress = progress
				}
				mu.Unlock()
			}
		}(img)
	}

	if isFullSync {
		for _, filePath := range filesToDelete {
			wg.Add(1)
			go func(path string) {
				defer wg.Done()

				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				if err := client.Delete(path); err != nil {
					logger.Error("[NodeImage->%s] 删除失败 %s: %v", client.GetName(), filepath.Base(path), err)
					mu.Lock()
					deleteErrCount++
					mu.Unlock()
				} else {
					logger.Info("[NodeImage->%s] 删除成功: %s", client.GetName(), filepath.Base(path))
					mu.Lock()
					deleteCount++
					mu.Unlock()
				}
			}(filePath)
		}
	}

	wg.Wait()

	if uploadCount > 0 || deleteCount > 0 {
		InvalidateWebdavCache()
	}

	logger.Info("[NodeImage->%s] 同步完成 - 上传: %d (失败: %d), 删除: %d (失败: %d)",
		client.GetName(), uploadCount, uploadErrCount, deleteCount, deleteErrCount)

	if uploadErrCount > 0 || deleteErrCount > 0 {
		return fmt.Errorf("部分操作失败: 上传失败 %d, 删除失败 %d", uploadErrCount, deleteErrCount)
	}

	return nil
}

func (e *Executor) uploadImage(client *webdav.EnhancedClient, img nodeimage.ImageInfo, remotePath string) error {
	const maxRetries = 3
	const retryDelay = 2 * time.Second

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		reader, err := nodeimage.NewClient("", "", "").DownloadImageStream(img.URL)
		if err != nil {
			lastErr = fmt.Errorf("下载失败: %w", err)
			if attempt < maxRetries {
				time.Sleep(retryDelay)
				continue
			}
			return lastErr
		}

		if err := client.UploadStream(reader, img.Size, remotePath); err != nil {
			reader.Close()
			lastErr = fmt.Errorf("上传失败: %w", err)
			if attempt < maxRetries {
				time.Sleep(retryDelay)
				continue
			}
			return lastErr
		}
		reader.Close()
		return nil
	}

	return lastErr
}

func (e *Executor) ExecuteTask(task interface{}) error {
	switch t := task.(type) {
	case *config.LocalBackupTask:
		return e.ExecuteLocalTask(t, nil)
	case *config.NodeImageSyncTask:
		return e.ExecuteNodeImageTask(t, nil)
	default:
		return fmt.Errorf("未知的任务类型: %T", task)
	}
}

func CreateWebDAVClients(cfg *config.Config) map[string]*webdav.EnhancedClient {
	clients := make(map[string]*webdav.EnhancedClient)

	for _, wd := range cfg.WebDAV {
		client := webdav.NewEnhancedClient(webdav.EnhancedConfig{
			Name:     wd.Name,
			URL:      wd.URL,
			Username: wd.Username,
			Password: wd.Password,
			Timeout:  wd.Timeout,
		})
		clients[wd.Name] = client
	}

	return clients
}

func ExecuteLocalBackup(task *config.LocalBackupTask, clients map[string]*webdav.EnhancedClient, cfg *config.Config) error {
	executor := NewExecutor(cfg)
	return executor.ExecuteLocalTask(task, clients)
}

func ExecuteNodeImageSync(task *config.NodeImageSyncTask, clients map[string]*webdav.EnhancedClient, cfg *config.Config) error {
	executor := NewExecutor(cfg)
	return executor.ExecuteNodeImageTask(task, clients)
}
