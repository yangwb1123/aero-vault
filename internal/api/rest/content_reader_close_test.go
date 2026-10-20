package rest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type contentCloseProbe struct {
	io.Reader
	closes  int
	err     error
	onClose func()
}

func (p *contentCloseProbe) Close() error {
	p.closes++
	if p.onClose != nil {
		p.onClose()
	}
	return p.err
}

func TestContentReaderCloseCachesSuccessAndFailure(t *testing.T) {
	for _, closeErr := range []error{nil, errors.New("close failure")} {
		probe := &contentCloseProbe{Reader: strings.NewReader("body"), err: closeErr}
		reader := &objectContentReader{ctx: context.Background(), source: probe}
		for i := 0; i < 3; i++ {
			if err := reader.Close(); err != closeErr {
				t.Errorf("close result changed: got=%v want=%v", err, closeErr)
			}
		}
		if probe.closes != 1 {
			t.Errorf("underlying source closed %d times, want 1", probe.closes)
		}
	}
}

func TestContentReaderCloseReportedErrorAbortsWithoutDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	handler := &Handler{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	request := httptest.NewRequest(http.MethodGet, "/v1/files/key", nil)
	probe := &contentCloseProbe{Reader: strings.NewReader("body"), err: errors.New("private backend s3://credentials@host/object")}
	reader := &objectContentReader{ctx: request.Context(), source: probe}
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Errorf("close failure was swallowed or changed: panic=%v", recovered)
		}
		if probe.closes != 1 || !strings.Contains(logs.String(), "source_close") || strings.Contains(logs.String(), "private") {
			t.Errorf("close failure duplicated or exposed diagnostics: closes=%d logs=%q", probe.closes, logs.String())
		}
	}()
	defer reader.Close()
	handler.serveOpenedContent(httptest.NewRecorder(), request, reader, repository.Object{Size: 4}, false)
}

func TestContentReaderCloseDoesNotReplacePriorCopyFailure(t *testing.T) {
	for _, copyErr := range []error{errors.New("private read or write failure"), nil} {
		assertContentReaderCopyFailure(t, copyErr)
	}
}

func assertContentReaderCopyFailure(t *testing.T, copyErr error) {
	t.Helper()
	var logs bytes.Buffer
	handler := &Handler{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	probe := &contentCloseProbe{Reader: strings.NewReader("body"), err: errors.New("private close failure")}
	reader := &objectContentReader{ctx: request.Context(), source: probe}
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Errorf("primary failure changed: panic=%v", recovered)
		}
		if probe.closes != 1 || strings.Contains(logs.String(), "source_close") || strings.Contains(logs.String(), "private") {
			t.Errorf("cleanup replaced primary error: closes=%d logs=%q", probe.closes, logs.String())
		}
	}()
	defer reader.Close()
	handler.finishContentCopy(request, reader, 0, 4, copyErr)
}

func TestContentReaderCloseCancellationKeepsExistingOutcome(t *testing.T) {
	for _, duringClose := range []bool{false, true} {
		var logs bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		probe := &contentCloseProbe{Reader: strings.NewReader("body"), err: errors.New("private close failure")}
		if duringClose {
			probe.onClose = cancel
		} else {
			cancel()
		}
		reader := &objectContentReader{ctx: ctx, source: probe}
		handler := &Handler{logger: slog.New(slog.NewTextHandler(&logs, nil))}
		handler.finishContentCopy(request, reader, 4, 4, nil)
		_ = reader.Close()
		cancel()
		if probe.closes != 1 || logs.Len() != 0 {
			t.Errorf("cancel became a close failure: closes=%d logs=%q", probe.closes, logs.String())
		}
	}
}

func TestContentReaderCloseMemoryReaderDoesNotRequireCloser(t *testing.T) {
	reader := &objectContentReader{ctx: context.Background(), source: strings.NewReader("body")}
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != "body" {
		t.Fatalf("memory reader changed: body=%q err=%v", body, err)
	}
	if err := reader.Close(); err != nil {
		t.Errorf("non-closer failed to close: %v", err)
	}
}
