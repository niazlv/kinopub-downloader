// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

package kinopub

import "errors"

// Ошибки, которые вызывающая сторона обязана различать, чтобы принимать
// решения: обновлять токен, ждать, или сдаваться.
var (
	// ErrUnauthorized — токен отвергнут. Клиент НИКОГДА не обновляет токен
	// сам: обновление требует client secret, а его место — в слое сессии,
	// не в HTTP-клиенте.
	ErrUnauthorized = errors.New("kinopub: unauthorized")

	// ErrNotFound — запрошенной сущности нет.
	ErrNotFound = errors.New("kinopub: not found")

	// ErrRateLimited — площадка попросила притормозить. Для краулера это
	// сигнал взять паузу, а не повод ретраить немедленно.
	ErrRateLimited = errors.New("kinopub: rate limited")
)

// APIError — ответ, который не уложился в известные категории. Несёт код,
// чтобы вызывающий мог отличить временную пятисотку от постоянной ошибки.
type APIError struct {
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return "kinopub: " + e.Path + " returned HTTP " + itoa(e.Status)
}

// Temporary сообщает, есть ли смысл повторить запрос.
func (e *APIError) Temporary() bool { return e.Status >= 500 || e.Status == 429 }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
