package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"webdav-backup/config"
	"webdav-backup/logger"
)

type Backup struct {
	TempDir string
}

func New(tempDir string) *Backup {
	return &Backup{TempDir: tempDir}
}

func (b *Backup) CreateTask(task *config.BackupTask, password string) (string, error) {
	if err := os.MkdirAll(b.TempDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	timestamp := time.Now().Format("20060102_150405.000")
	taskDir := filepath.Join(b.TempDir, task.Name)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create task dir: %w", err)
	}

	tarFile := filepath.Join(taskDir, fmt.Sprintf("%s_%s.tar.gz", task.Name, timestamp))
	encFile := filepath.Join(taskDir, fmt.Sprintf("%s_%s.tar.gz.enc", task.Name, timestamp))

	logger.Info("[%s] Step 1/3: Creating tar archive...", task.Name)
	if err := b.createTar(tarFile, task.Paths, task.Name); err != nil {
		return "", fmt.Errorf("failed to create tar: %w", err)
	}

	tarInfo, _ := os.Stat(tarFile)
	if tarInfo != nil {
		logger.Info("[%s] Archive created: %s (%.2f MB)", task.Name, tarFile, float64(tarInfo.Size())/1024/1024)
	}

	defer os.Remove(tarFile)

	logger.Info("[%s] Step 2/3: Encrypting archive with AES-256-GCM...", task.Name)
	if err := b.encrypt(tarFile, encFile, password, task.Name); err != nil {
		return "", fmt.Errorf("failed to encrypt: %w", err)
	}

	encInfo, _ := os.Stat(encFile)
	if encInfo != nil {
		logger.Info("[%s] Step 3/3: Backup ready: %s (%.2f MB)", task.Name, filepath.Base(encFile), float64(encInfo.Size())/1024/1024)
	}

	return encFile, nil
}

func (b *Backup) createTar(output string, items []config.BackupItem, taskName string) error {
	file, err := os.Create(output)
	if err != nil {
		return err
	}
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	totalFiles := 0
	processedFiles := 0

	for _, item := range items {
		filepath.Walk(item.Path, func(_ string, info os.FileInfo, _ error) error {
			if !info.IsDir() {
				totalFiles++
			}
			return nil
		})
	}

	logger.Info("[%s] Found %d files to backup", taskName, totalFiles)

	for _, item := range items {
		if err := b.addToTar(tarWriter, item.Path, item.Type, &processedFiles, totalFiles, taskName); err != nil {
			logger.Warn("[%s] Failed to add %s to archive: %v", taskName, item.Path, err)
		}
	}

	return nil
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
			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}
			return nil
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
				progress := float64(*processed) / float64(total) * 100
				logger.Info("[%s] Archiving progress: %d/%d files (%.1f%%)", taskName, *processed, total, progress)
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
			progress := float64(*processed) / float64(total) * 100
			logger.Info("[%s] Archiving progress: %d/%d files (%.1f%%)", taskName, *processed, total, progress)
		}
	}
	return err
}

func (b *Backup) encrypt(input, output, password string, taskName string) error {
	key := deriveKey(password)

	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}

	logger.Info("[%s] Reading file for encryption...", taskName)
	data, err := os.ReadFile(input)
	if err != nil {
		return err
	}

	logger.Info("[%s] Encrypting %d bytes...", taskName, len(data))
	encrypted := gcm.Seal(nonce, nonce, data, nil)

	logger.Info("[%s] Writing encrypted file...", taskName)
	return os.WriteFile(output, encrypted, 0644)
}

func deriveKey(password string) []byte {
	hash := sha256.Sum256([]byte(password))
	return hash[:]
}

func Decrypt(input, output, password string) error {
	key := deriveKey(password)

	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(input)
	if err != nil {
		return err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return fmt.Errorf("encrypted data too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	decrypted, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return err
	}

	return os.WriteFile(output, decrypted, 0644)
}
