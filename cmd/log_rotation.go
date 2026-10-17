package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	maxLogFileSize int64 = 10 * 1024 * 1024
	maxLogBackups        = 3
)

type rotatingFile struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	backups int
	file    *os.File
	size    int64
}

func newRotatingFile(path string, maxSize int64, backups int) (*rotatingFile, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("日志文件上限必须大于零")
	}
	if backups < 0 {
		return nil, fmt.Errorf("日志备份数量不能小于零")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败：%w", err)
	}
	if err := trimRotatingLogs(path, maxSize, backups); err != nil {
		return nil, err
	}
	writer := &rotatingFile{path: path, maxSize: maxSize, backups: backups}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	writer.file, writer.size = file, info.Size()
	if writer.size >= maxSize {
		if err := writer.rotate(); err != nil {
			if writer.file != nil {
				_ = writer.file.Close()
			}
			return nil, err
		}
	}
	return writer, nil
}

func trimRotatingLogs(path string, maxSize int64, backups int) error {
	paths := make([]string, 0, backups+1)
	paths = append(paths, path)
	for index := 1; index <= backups; index++ {
		paths = append(paths, fmt.Sprintf("%s.%d", path, index))
	}
	for _, logPath := range paths {
		file, err := os.OpenFile(logPath, os.O_RDWR, 0)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("打开日志文件以检查大小失败：%w", err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("读取日志文件状态失败：%w", err)
		}
		if info.Size() <= maxSize {
			if err := file.Close(); err != nil {
				return fmt.Errorf("关闭日志文件失败：%w", err)
			}
			continue
		}
		tail := make([]byte, maxSize)
		if _, err := file.ReadAt(tail, info.Size()-maxSize); err != nil {
			_ = file.Close()
			return fmt.Errorf("读取日志文件尾部失败：%w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("关闭日志文件失败：%w", err)
		}
		if err := os.WriteFile(logPath, tail, 0o600); err != nil {
			return fmt.Errorf("裁剪超限日志文件失败：%w", err)
		}
	}
	return nil
}

func (writer *rotatingFile) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.file == nil {
		return 0, os.ErrClosed
	}
	originalSize := len(data)
	if int64(len(data)) > writer.maxSize {
		data = data[len(data)-int(writer.maxSize):]
	}
	if writer.size+int64(len(data)) > writer.maxSize {
		if err := writer.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := writer.file.Write(data)
	writer.size += int64(n)
	if err == nil && n == len(data) {
		return originalSize, nil
	}
	return n, err
}

func (writer *rotatingFile) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.file == nil {
		return nil
	}
	err := writer.file.Close()
	writer.file = nil
	return err
}

func (writer *rotatingFile) rotate() error {
	file := writer.file
	writer.file = nil
	if err := file.Close(); err != nil {
		return writer.resumeAfterRotateError(err)
	}
	if writer.backups == 0 {
		if err := os.Remove(writer.path); err != nil && !os.IsNotExist(err) {
			return writer.resumeAfterRotateError(err)
		}
	} else {
		last := fmt.Sprintf("%s.%d", writer.path, writer.backups)
		if err := os.Remove(last); err != nil && !os.IsNotExist(err) {
			return writer.resumeAfterRotateError(err)
		}
		for index := writer.backups - 1; index >= 1; index-- {
			oldPath := fmt.Sprintf("%s.%d", writer.path, index)
			newPath := fmt.Sprintf("%s.%d", writer.path, index+1)
			if err := os.Rename(oldPath, newPath); err != nil && !os.IsNotExist(err) {
				return writer.resumeAfterRotateError(err)
			}
		}
		if err := os.Rename(writer.path, writer.path+".1"); err != nil && !os.IsNotExist(err) {
			return writer.resumeAfterRotateError(err)
		}
	}
	file, err := os.OpenFile(writer.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return writer.resumeAfterRotateError(err)
	}
	writer.file, writer.size = file, 0
	return nil
}

func (writer *rotatingFile) resumeAfterRotateError(cause error) error {
	file, err := os.OpenFile(writer.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("轮转日志失败：%w；重新打开当前日志文件失败：%v", cause, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("轮转日志失败：%w；读取当前日志文件状态失败：%v", cause, err)
	}
	writer.file, writer.size = file, info.Size()
	return cause
}
