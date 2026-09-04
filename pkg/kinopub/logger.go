// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

package kinopub

// Logger — минимальный интерфейс логирования.
//
// Собственный, а не domain.Logger из internal/, по двум причинам: внешний
// потребитель не может назвать внутренний тип, и библиотека не должна
// навязывать чужую систему логирования. Адаптер к любому логгеру — три строки
// на стороне вызывающего.
type Logger interface {
	Debug(msg string, keyvals ...any)
	Error(msg string, keyvals ...any)
}

// LoggerFunc позволяет подсунуть функцию вместо реализации интерфейса.
type LoggerFunc func(level, msg string, keyvals ...any)

func (f LoggerFunc) Debug(msg string, kv ...any) { f("debug", msg, kv...) }
func (f LoggerFunc) Error(msg string, kv ...any) { f("error", msg, kv...) }

type nopLogger struct{}

func (nopLogger) Debug(string, ...any) {}
func (nopLogger) Error(string, ...any) {}
