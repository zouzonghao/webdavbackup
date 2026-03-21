package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"webdav-backup/config"
	"webdav-backup/logger"
)

type Backup struct{}

func New() *Backup {
	return &Backup{}
}

func (b *Backup) CreateStream(task *config.BackupTask) (io.ReadCloser, error) {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()

		gzWriter := gzip.NewWriter(pw)
		defer gzWriter.Close()

		tarWriter := tar.NewWriter(gzWriter)
		defer tarWriter.Close()

		totalFiles := 0
		for _, item := range task.Paths {
			filepath.Walk(item.Path, func(_ string, info os.FileInfo, _ error) error {
				if !info.IsDir() {
					totalFiles++
				}
				return nil
			})
		}

		logger.Info("[%s] Found %d files to backup", task.Name, totalFiles)

		processed := 0
		for _, item := range task.Paths {
			if err := b.addToTar(tarWriter, item.Path, item.Type, &processed, totalFiles, task.Name); err != nil {
				logger.Warn("[%s] Failed to add %s: %v", task.Name, item.Path, err)
			}
		}

		logger.Info("[%s] Archive complete: %d files", task.Name, processed)
	}()

	return pr, nil
}

func (b *Backup) addToTar(tarWriter *tar.Writer, path string, itemType string, processed *int, total int, taskName string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot access %s: %w", path, err)
	}

	if itemType == "" {
		if info.IsDir() {
			itemType = "dir"
		} else {
			itemType = "file"
		}
	}

	if itemType == "dir" || info.IsDir() {
		return b.addDirToTar(tarWriter, path, processed, total, taskName)
	}

	return b.addFileToTar(tarWriter, path, processed, total, taskName)
}

func (b *Backup) addDirToTar(tarWriter *tar.Writer, dirPath string, processed *int, total int, taskName string) error {
	return filepath.Walk(dirPath, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		header.Name = filePath

		if info.IsDir() {
			return tarWriter.WriteHeader(header)
		}

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(tarWriter, file)
		if err == nil {
			*processed++
			if *processed%100 == 0 || *processed == total {
				logger.Info("[%s] Progress: %d/%d files (%.1f%%)", taskName, *processed, total, float64(*processed)/float64(total)*100)
			}
		}
		return err
	})
}

func (b *Backup) addFileToTar(tarWriter *tar.Writer, filePath string, processed *int, total int, taskName string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}

	header.Name = filePath

	if err := tarWriter.WriteHeader(header); err != nil {
		return err
	}

	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(tarWriter, file)
	if err == nil {
		*processed++
		if total > 0 {
			logger.Info("[%s] Progress: %d/%d files (%.1f%%)", taskName, *processed, total, float64(*processed)/float64(total)*100)
		}
	}
	return err
}
