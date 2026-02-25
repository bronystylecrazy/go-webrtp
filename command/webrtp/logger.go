package main

import (
	"fmt"
	"log"
)

type Logger struct {
	logger *log.Logger
	prefix string
}

func NewLogger(prefix string, logger *log.Logger) *Logger {
	if logger == nil {
		logger = log.Default()
	}
	return &Logger{logger: logger, prefix: prefix}
}

func (l *Logger) Print(v ...interface{}) {
	l.logger.Print(l.prefix + " " + fmt.Sprint(v...))
}

func (l *Logger) Printf(format string, v ...interface{}) {
	l.logger.Print(l.prefix + " " + fmt.Sprintf(format, v...))
}
