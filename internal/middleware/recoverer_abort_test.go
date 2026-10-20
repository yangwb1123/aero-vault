package middleware

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecovererOtherPanicsStillBecome500(t *testing.T) {
	for _, value := range []any{"boom", []int{1, 2}, fmt.Errorf("not the exact sentinel: %w", http.ErrAbortHandler)} {
		handler := Recoverer(quietLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic(value)
		}))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		if recorder.Code != http.StatusInternalServerError || recorder.Body.String() != "internal server error\n" {
			t.Errorf("application panic changed: panic=%v code=%d body=%q", value, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRecovererPreservesAbortHandlerWithoutWritingOrLoggingPanic(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	handler := Recoverer(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "selected prefix")
		panic(http.ErrAbortHandler)
	}))
	recorder := httptest.NewRecorder()
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Errorf("abort swallowed/changed: got=%v want=http.ErrAbortHandler", recovered)
		}
		if recorder.Body.String() != "selected prefix" || logs.Len() != 0 {
			t.Errorf("abort became an application panic: body=%q logs=%q", recorder.Body.String(), logs.String())
		}
	}()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/files/key", nil))
}
