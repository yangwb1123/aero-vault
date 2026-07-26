package config

type AuthConfig struct {
	Keys                string
	JWTSecret           string
	JWTIssuer           string // AUTH_JWT_ISSUER; when set, JWTs must carry a matching "iss" claim (and issued tokens are stamped with it)
	JWKSEndpoint        string // AUTH_JWKS_ENDPOINT; OIDC JWKS URL for RS256 JWT verification
	JWKSKeyTTLSeconds   int    // AUTH_JWKS_KEY_TTL; JWKS key cache TTL (default 3600)
	AnonymousPublicRead bool
	SigV4Credentials    string // accessKey:secretKey:tenant[:scope+scope],...
	PersistKeys         bool   // back runtime API keys with the repository (hashed, survive restart)
	KeyCacheTTLSeconds  int    // >0 caches persisted-key lookups for this many seconds (revokes bounded by TTL across replicas)
}

type CORSCfg struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	ExposeHeaders  []string // CORS_EXPOSE_HEADERS; response headers browsers may read
}

type RateLimitCfg struct {
	RPS        float64
	Burst      float64
	AIRPS      float64
	AIBurst    float64
	AdminRPS   float64 // ADMIN_RATE_LIMIT_RPS; admin API rate limit (0 = unlimited)
	AdminBurst float64 // ADMIN_RATE_LIMIT_BURST; admin API burst (0 = unlimited)
}
