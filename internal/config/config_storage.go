package config

type StorageConfig struct {
	Backend          string
	SSERewrapOnStart bool
	DefaultClass     string // STORAGE_DEFAULT_CLASS; "" = STANDARD
	ConnectTimeout   int    // seconds; STORAGE_CONNECT_TIMEOUT
	ReadTimeout      int    // seconds; STORAGE_READ_TIMEOUT
	WriteTimeout     int    // seconds; STORAGE_WRITE_TIMEOUT

	// Read-verification settings (STORAGE_VERIFY_*).
	VerifyOnRead  bool  // STORAGE_VERIFY_ON_READ; enable on-read ETag verification
	VerifyMaxSize int64 // STORAGE_VERIFY_MAX_SIZE; bytes threshold for full vs sample verification
	VerifySample  bool  // STORAGE_VERIFY_SAMPLE; enable sampling for large objects

	// Circuit breaker settings (per-storage-backend).
	CBFailureThreshold int  // STORAGE_CB_FAILURE_THRESHOLD; 0 = default (5)
	CBRecoveryTimeout  int  // STORAGE_CB_RECOVERY_TIMEOUT; seconds, 0 = default (30)
	CBHalfOpenMax      int  // STORAGE_CB_HALF_OPEN_MAX; 0 = default (1)
	CBEnabled          bool // STORAGE_CB_ENABLED; master switch

	Local LocalStorageConfig
	S3    S3StorageConfig
	OSS   OSSStorageConfig
	COS   COSStorageConfig
}

type OSSStorageConfig struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
}

type COSStorageConfig struct {
	BucketURL string
	SecretID  string
	SecretKey string
}

type LocalStorageConfig struct {
	Root        string
	PublicURL   string
	SignKey     string
	SSEKey      string
	SSEKeyfile  string
	SSEKeyURL   string
	SSEKeyToken string
	SSEKMSURL   string
	SSEKMSKeyID string
	SSEKMSToken string
}

type S3StorageConfig struct {
	Endpoint       string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	ForcePathStyle bool
}
