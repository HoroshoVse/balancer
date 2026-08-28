package engine

import (
	"sync"
	"time"
)

type LogLevel string

const (
	LevelInfo  LogLevel = "INFO"
	LevelWarn  LogLevel = "WARN"
	LevelError LogLevel = "ERROR"
)

type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     LogLevel  `json:"level"`
	Message   string    `json:"message"`
	LBName    string    `json:"lb_name,omitempty"`
}

type LogBuffer struct {
	mu       sync.Mutex
	logs     []LogEntry
	capacity int
	head     int
	count    int
}

func NewLogBuffer(capacity int) *LogBuffer {
	return &LogBuffer{
		logs:     make([]LogEntry, capacity),
		capacity: capacity,
	}
}

func (b *LogBuffer) addLog(level LogLevel, message string, lbName string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.logs[b.head] = LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
		LBName:    lbName,
	}
	b.head = (b.head + 1) % b.capacity
	if b.count < b.capacity {
		b.count++
	}
}

func (b *LogBuffer) Info(message string) {
	b.addLog(LevelInfo, message, "")
}

func (b *LogBuffer) Warn(message string) {
	b.addLog(LevelWarn, message, "")
}

func (b *LogBuffer) Error(message string) {
	b.addLog(LevelError, message, "")
}

func (b *LogBuffer) InfoLB(lbName, message string) {
	b.addLog(LevelInfo, message, lbName)
}

func (b *LogBuffer) WarnLB(lbName, message string) {
	b.addLog(LevelWarn, message, lbName)
}

func (b *LogBuffer) ErrorLB(lbName, message string) {
	b.addLog(LevelError, message, lbName)
}

func (b *LogBuffer) GetLogs() []LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := make([]LogEntry, 0, b.count)
	if b.count < b.capacity {
		for i := 0; i < b.count; i++ {
			result = append(result, b.logs[i])
		}
	} else {
		for i := 0; i < b.capacity; i++ {
			index := (b.head + i) % b.capacity
			result = append(result, b.logs[index])
		}
	}
	return result
}

var Logger = NewLogBuffer(1000)
