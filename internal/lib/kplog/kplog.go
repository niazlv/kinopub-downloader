// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package kplog связывает логгер приложения с минимальным интерфейсом
// публичной библиотеки.
//
// Адаптер живёт здесь, а не в pkg/kinopub, потому что направление
// зависимости именно такое: библиотека не знает про domain.Logger, это
// приложение знает про обе стороны. Три строки на стыке — цена того, что
// библиотека не навязывает внешнему потребителю чужую систему логирования.
package kplog

import (
	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/pkg/kinopub"
)

type adapter struct{ l domain.Logger }

// Adapt оборачивает логгер приложения. nil на входе даёт nil на выходе:
// WithLogger(nil) у библиотеки — законный no-op.
func Adapt(l domain.Logger) kinopub.Logger {
	if l == nil {
		return nil
	}
	return adapter{l: l.Component("kinopub-api")}
}

func (a adapter) Debug(msg string, kv ...any) { a.l.Debug(msg, fields(kv)...) }
func (a adapter) Error(msg string, kv ...any) { a.l.Error(msg, fields(kv)...) }

// fields переводит плоский список ключ-значение в поля домена.
// Непарный хвост не выбрасывается, а помечается: потерянный контекст
// в логе хуже кривого ключа.
func fields(kv []any) []domain.Field {
	out := make([]domain.Field, 0, (len(kv)+1)/2)
	for i := 0; i < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			key = "arg"
		}
		if i+1 >= len(kv) {
			out = append(out, domain.F(key, "(no value)"))
			break
		}
		out = append(out, domain.F(key, kv[i+1]))
	}
	return out
}
