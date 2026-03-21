package logger

import (
	"fmt"
	"log"
	"sync"
	"time"
)

type Logger struct {
	mu       sync.Mutex
	callback func(level, msg string)
}

var defaultLogger *Logger

func Init() {
	defaultLogger = &Logger{}
}

func SetLogCallback(cb func(level, msg string)) {
	if defaultLogger != nil {
		defaultLogger.mu.Lock()
		defaultLogger.callback = cb
		defaultLogger.mu.Unlock()
	}
}

func (l *Logger) log(level, format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] [%s] %s", timestamp, level, msg)
	log.Println(line)

	if l.callback != nil {
		l.callback(level, msg)
	}
}

func (l *Logger) Info(format string, args ...interface{}) {
	l.log("INFO", format, args...)
}

func (l *Logger) Error(format string, args ...interface{}) {
	l.log("ERROR", format, args...)
}

func (l *Logger) Warn(format string, args ...interface{}) {
	l.log("WARN", format, args...)
}

func (l *Logger) Debug(format string, args ...interface{}) {
	l.log("DEBUG", format, args...)
}

func Info(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.Info(format, args...)
	} else {
		log.Printf("[INFO] "+format, args...)
	}
}

func Error(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.Error(format, args...)
	} else {
		log.Printf("[ERROR] "+format, args...)
	}
}

func Warn(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.Warn(format, args...)
	} else {
		log.Printf("[WARN] "+format, args...)
	}
}

func Debug(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.Debug(format, args...)
	} else {
		log.Printf("[DEBUG] "+format, args...)
	}
}
