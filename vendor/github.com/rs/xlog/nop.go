package xlog

type nop struct{}

// NopLogger is an no-op implementation of xlog.Logger
var NopLogger = &nop{}

func (n nop) SetField(name string, value interface{}) {}

func (n nop) Debug(v ...interface{}) {}

func (n nop) Debugf(format string, v ...interface{}) {}

func (n nop) Info(v ...interface{}) {}

func (n nop) Infof(format string, v ...interface{}) {}

func (n nop) Warn(v ...interface{}) {}

func (n nop) Warnf(format string, v ...interface{}) {}

func (n nop) Error(v ...interface{}) {}

func (n nop) Errorf(format string, v ...interface{}) {}

func (n nop) Write(p []byte) (int, error) { return len(p), nil }