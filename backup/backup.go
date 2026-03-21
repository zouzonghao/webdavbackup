package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mzky/zip"

	"webdav-backup/config"
	"webdav-backup/logger"
)

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

func CreateStream(task *config.BackupTask) (*StreamResult, error) {
	var files []fileEntry
	var totalSize int64

	for _, item := range task.Paths {
		err := filepath.Walk(item.Path, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
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
			logger.Warn("[%s] Failed to walk %s: %v", task.Name, item.Path, err)
		}
	}

	fileCount := 0
	for _, f := range files {
		if !f.isDir {
			fileCount++
		}
	}

	logger.Info("[%s] Found %d files, total size: %.2f MB", task.Name, fileCount, float64(totalSize)/1024/1024)

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
					logger.Warn("[%s] Failed to add dir %s: %v", task.Name, f.path, err)
				}
			} else {
				if err := addFileToZip(zipWriter, f.path, f.info, &processed, fileCount, task.Name, task.EncryptPwd); err != nil {
					logger.Warn("[%s] Failed to add %s: %v", task.Name, f.path, err)
				}
			}
		}

		logger.Info("[%s] Archive complete: %d files", task.Name, processed)
		sizeChan <- cw.count
	}()

	return &StreamResult{
		Stream:    pr,
		TotalSize: totalSize,
		FileCount: fileCount,
		SizeChan:  sizeChan,
	}, nil
}

func addFileToZip(zipWriter *zip.Writer, filePath string, info os.FileInfo, processed *int, total int, taskName string, password string) error {
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("failed to create file header: %w", err)
	}
	header.Name = filePath
	header.Method = zip.Deflate

	var writer io.Writer
	if password != "" {
		writer, err = zipWriter.Encrypt(header.Name, password, zip.AES256Encryption)
		if err != nil {
			return fmt.Errorf("failed to create encrypted entry: %w", err)
		}
	} else {
		writer, err = zipWriter.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("failed to create entry: %w", err)
		}
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	_, err = io.Copy(writer, file)
	if err != nil {
		return fmt.Errorf("failed to copy file content: %w", err)
	}

	*processed++
	if *processed%100 == 0 || *processed == total {
		logger.Info("[%s] Progress: %d/%d files (%.1f%%)", taskName, *processed, total, float64(*processed)/float64(total)*100)
	}
	return nil
}
