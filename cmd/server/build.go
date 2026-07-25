package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/antivirus"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

func buildStorage(ctx context.Context, cfg *config.Config) (storage.Storage, error) {
	return buildStorageFrom(ctx, cfg.Storage)
}

func buildStorageFrom(ctx context.Context, sc config.StorageConfig) (storage.Storage, error) {
	service.WithDefaultStorageClass(sc.DefaultClass)
	fc := storage.NewDefaultFactoryConfig()
	fc.Kind = storage.BackendKind(sc.Backend)
	if sc.ConnectTimeout > 0 {
		fc.Timeouts.ConnectTimeout = time.Duration(sc.ConnectTimeout) * time.Second
	}
	if sc.ReadTimeout > 0 {
		fc.Timeouts.ReadTimeout = time.Duration(sc.ReadTimeout) * time.Second
	}
	if sc.WriteTimeout > 0 {
		fc.Timeouts.WriteTimeout = time.Duration(sc.WriteTimeout) * time.Second
	}
	switch fc.Kind {
	case storage.BackendLocal:
		fc.Local = storage.LocalConfig{
			Root:        sc.Local.Root,
			PublicURL:   sc.Local.PublicURL,
			SignKey:     sc.Local.SignKey,
			SSEKey:      sc.Local.SSEKey,
			SSEKeyfile:  sc.Local.SSEKeyfile,
			SSEKeyURL:   sc.Local.SSEKeyURL,
			SSEKeyToken: sc.Local.SSEKeyToken,
			SSEKMSURL:   sc.Local.SSEKMSURL,
			SSEKMSKeyID: sc.Local.SSEKMSKeyID,
			SSEKMSToken: sc.Local.SSEKMSToken,
		}
	case storage.BackendS3:
		fc.S3 = storage.S3Config{
			Endpoint:       sc.S3.Endpoint,
			Region:         sc.S3.Region,
			Bucket:         sc.S3.Bucket,
			AccessKey:      sc.S3.AccessKey,
			SecretKey:      sc.S3.SecretKey,
			ForcePathStyle: sc.S3.ForcePathStyle,
		}
	case storage.BackendOSS:
		fc.OSS = storage.OSSConfig{
			Endpoint:  sc.OSS.Endpoint,
			Bucket:    sc.OSS.Bucket,
			AccessKey: sc.OSS.AccessKey,
			SecretKey: sc.OSS.SecretKey,
		}
	case storage.BackendCOS:
		fc.COS = storage.COSConfig{
			BucketURL: sc.COS.BucketURL,
			SecretID:  sc.COS.SecretID,
			SecretKey: sc.COS.SecretKey,
		}
	}
	fc.CircuitBreaker = storage.CBConfig{
		Enabled:             sc.CBEnabled,
		FailureThreshold:    sc.CBFailureThreshold,
		RecoveryTimeout:     time.Duration(sc.CBRecoveryTimeout) * time.Second,
		HalfOpenMaxRequests: sc.CBHalfOpenMax,
	}
	if fc.CircuitBreaker.Enabled {
		if fc.CircuitBreaker.FailureThreshold <= 0 {
			fc.CircuitBreaker.FailureThreshold = 5
		}
		if fc.CircuitBreaker.RecoveryTimeout <= 0 {
			fc.CircuitBreaker.RecoveryTimeout = 30 * time.Second
		}
		if fc.CircuitBreaker.HalfOpenMaxRequests <= 0 {
			fc.CircuitBreaker.HalfOpenMaxRequests = 1
		}
	}
	return storage.NewFromConfig(ctx, fc)
}

func buildPrometheus(cfg *config.Config, logger *slog.Logger) http.Handler {
	if !cfg.Telemetry.PrometheusEnabled {
		return nil
	}
	h, err := telemetry.EnablePrometheus()
	if err != nil {
		logger.Warn("prometheus exporter failed", "err", err)
		return nil
	}
	logger.Info("prometheus /metrics enabled")
	return h
}

func registerGauges(repo repository.Repository) {
	telemetry.RegisterQueueDepthGauge(func(ctx context.Context) int64 {
		n, _ := repo.CountJobsByStatus(ctx, "pending")
		return int64(n)
	})
	telemetry.RegisterStorageGauges(func(ctx context.Context) []telemetry.TenantStorage {
		qs, _ := repo.ListTenantQuotas(ctx)
		out := make([]telemetry.TenantStorage, 0, len(qs))
		for _, q := range qs {
			out = append(out, telemetry.TenantStorage{Tenant: q.TenantID, Bytes: q.UsedBytes, Objects: q.UsedObjects})
		}
		return out
	})
	telemetry.RegisterStorageClassGauge(func(ctx context.Context, tenant string) map[string]int64 {
		counts, _ := repo.StorageClassCounts(ctx, tenant)
		return counts
	})
}

func buildEmbedder(cfg *config.Config, logger *slog.Logger) ai.Embedder {
	if !cfg.AI.Enabled {
		return nil
	}
	if cfg.AI.Provider == "http" && cfg.AI.Endpoint != "" {
		logger.Info("embedder: http", "endpoint", cfg.AI.Endpoint, "model", cfg.AI.Model)
		return ai.NewHTTPEmbedder(cfg.AI.Endpoint, cfg.AI.Model, cfg.AI.APIKey, cfg.AI.Dim)
	}
	logger.Info("embedder: hash (built-in)", "dim", cfg.AI.Dim)
	return ai.NewHashEmbedder(cfg.AI.Dim)
}

func buildScanner(cfg *config.Config, logger *slog.Logger) antivirus.Scanner {
	if cfg.Antivirus.Provider == "http" && cfg.Antivirus.Endpoint != "" {
		logger.Info("antivirus: http scanner", "endpoint", cfg.Antivirus.Endpoint)
		return antivirus.NewHTTPScanner(cfg.Antivirus.Endpoint, cfg.Antivirus.APIKey)
	}
	logger.Info("antivirus: built-in signature scanner")
	return antivirus.NewSignatureScanner(nil)
}

func buildLLM(cfg *config.Config, logger *slog.Logger) ai.LLM {
	if !cfg.AI.Enabled {
		return nil
	}
	if cfg.AI.ChatProvider == "http" && cfg.AI.ChatEndpoint != "" {
		logger.Info("llm: http", "endpoint", cfg.AI.ChatEndpoint, "model", cfg.AI.ChatModel)
		return ai.NewHTTPLLM(cfg.AI.ChatEndpoint, cfg.AI.ChatModel, cfg.AI.ChatAPIKey)
	}
	if cfg.AI.ChatProvider == "mock" {
		logger.Info("llm: mock (echo)")
		return ai.MockLLM{}
	}
	return nil
}

func buildReranker(cfg *config.Config, logger *slog.Logger) ai.Reranker {
	if cfg.AI.RerankProvider == "http" && cfg.AI.RerankEndpoint != "" {
		logger.Info("reranker: http", "endpoint", cfg.AI.RerankEndpoint, "model", cfg.AI.RerankModel)
		return ai.NewHTTPReranker(cfg.AI.RerankEndpoint, cfg.AI.RerankModel, cfg.AI.RerankAPIKey)
	}
	if cfg.AI.RerankProvider == "heuristic" {
		logger.Info("reranker: heuristic (dep-free fallback)")
		return ai.HeuristicReranker{}
	}
	return nil
}
