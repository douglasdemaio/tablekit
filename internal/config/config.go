package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

type Config struct {
	Env      string
	HTTPAddr string

	BaseDomain   string
	PublicScheme string

	DatabaseURL        string
	MigrateDatabaseURL string

	EncryptionKey []byte

	SessionTTL         time.Duration
	CustomerSessionTTL time.Duration
	MFAPendingTTL      time.Duration

	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPTLS      bool

	WebAuthnRPID    string
	WebAuthnRPName  string
	WebAuthnOrigins []string

	CookieSecure   bool
	BillingEnabled bool
	TrustProxy     bool

	AppDBUser     string
	AppDBPassword string
	DBName        string

	Storage  StorageConfig
	MediaDir string
}

// StorageConfig configures the S3-compatible object store used for media.
type StorageConfig struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

func (c *Config) IsProduction() bool { return c.Env == EnvProduction }

// StorageEnabled reports whether an S3-compatible object store is configured.
func (c *Config) StorageEnabled() bool {
	if c.MediaDir != "" {
		return true
	}
	return c.Storage.AccessKey != "" && c.Storage.SecretKey != "" && c.Storage.Bucket != ""
}

func Load() (*Config, error) {
	c := &Config{
		Env:                env("TABLEKIT_ENV", EnvDevelopment),
		HTTPAddr:           env("TABLEKIT_HTTP_ADDR", ":8080"),
		BaseDomain:         strings.TrimPrefix(strings.ToLower(env("TABLEKIT_BASE_DOMAIN", "tablekit.de")), "."),
		PublicScheme:       strings.ToLower(env("TABLEKIT_PUBLIC_SCHEME", "https")),
		DatabaseURL:        os.Getenv("TABLEKIT_DATABASE_URL"),
		MigrateDatabaseURL: os.Getenv("TABLEKIT_MIGRATE_DATABASE_URL"),
		WebAuthnRPID:       env("TABLEKIT_WEBAUTHN_RP_ID", "localhost"),
		WebAuthnRPName:     env("TABLEKIT_WEBAUTHN_RP_NAME", "tablekit"),
		AppDBUser:          env("TABLEKIT_APP_DB_USER", "tablekit_app"),
		AppDBPassword:      os.Getenv("TABLEKIT_APP_DB_PASSWORD"),
		DBName:             env("TABLEKIT_DB_NAME", "tablekit"),
		Storage: StorageConfig{
			Endpoint:  env("TABLEKIT_S3_ENDPOINT", "localhost:9000"),
			Region:    env("TABLEKIT_S3_REGION", "us-east-1"),
			Bucket:    env("TABLEKIT_S3_BUCKET", "tablekit-media"),
			AccessKey: os.Getenv("TABLEKIT_S3_ACCESS_KEY"),
			SecretKey: os.Getenv("TABLEKIT_S3_SECRET_KEY"),
		},
	}
	c.Storage.UseSSL = boolEnv("TABLEKIT_S3_USE_SSL", c.IsProduction())
	c.MediaDir = strings.TrimSpace(os.Getenv("TABLEKIT_MEDIA_DIR"))
	if c.MediaDir != "" && c.IsProduction() {
		return nil, errors.New("TABLEKIT_MEDIA_DIR is only allowed in development")
	}

	if c.Env != EnvDevelopment && c.Env != EnvProduction {
		return nil, fmt.Errorf("TABLEKIT_ENV must be %q or %q", EnvDevelopment, EnvProduction)
	}
	if c.PublicScheme != "http" && c.PublicScheme != "https" {
		return nil, errors.New("TABLEKIT_PUBLIC_SCHEME must be http or https")
	}
	if c.BaseDomain == "" {
		return nil, errors.New("TABLEKIT_BASE_DOMAIN is required")
	}
	if c.DatabaseURL == "" {
		return nil, errors.New("TABLEKIT_DATABASE_URL is required")
	}
	if _, err := url.Parse(c.DatabaseURL); err != nil {
		return nil, fmt.Errorf("TABLEKIT_DATABASE_URL: %w", err)
	}

	var err error
	if c.SessionTTL, err = durationEnv("TABLEKIT_SESSION_TTL", 12*time.Hour); err != nil {
		return nil, err
	}
	if c.MFAPendingTTL, err = durationEnv("TABLEKIT_MFA_PENDING_TTL", 10*time.Minute); err != nil {
		return nil, err
	}
	if c.CustomerSessionTTL, err = durationEnv("TABLEKIT_CUSTOMER_SESSION_TTL", 30*24*time.Hour); err != nil {
		return nil, err
	}
	if c.CustomerSessionTTL < time.Hour || c.CustomerSessionTTL > 90*24*time.Hour {
		return nil, errors.New("TABLEKIT_CUSTOMER_SESSION_TTL must be between 1h and 2160h")
	}
	if c.SessionTTL < time.Minute || c.SessionTTL > 30*24*time.Hour {
		return nil, errors.New("TABLEKIT_SESSION_TTL must be between 1m and 720h")
	}
	if c.MFAPendingTTL < time.Minute || c.MFAPendingTTL > time.Hour {
		return nil, errors.New("TABLEKIT_MFA_PENDING_TTL must be between 1m and 1h")
	}

	c.SMTPHost = strings.TrimSpace(os.Getenv("TABLEKIT_SMTP_HOST"))
	c.SMTPPort = intEnv("TABLEKIT_SMTP_PORT", 587)
	c.SMTPUsername = strings.TrimSpace(os.Getenv("TABLEKIT_SMTP_USERNAME"))
	c.SMTPPassword = os.Getenv("TABLEKIT_SMTP_PASSWORD")
	c.SMTPFrom = strings.TrimSpace(os.Getenv("TABLEKIT_SMTP_FROM"))
	if c.SMTPFrom == "" {
		c.SMTPFrom = "tablekit <no-reply@" + c.BaseDomain + ">"
	}
	c.SMTPTLS = boolEnv("TABLEKIT_SMTP_TLS", true)

	c.CookieSecure = boolEnv("TABLEKIT_COOKIE_SECURE", c.IsProduction())
	c.BillingEnabled = boolEnv("TABLEKIT_BILLING_ENABLED", false)
	c.TrustProxy = boolEnv("TABLEKIT_TRUST_PROXY", false)

	rawKey := strings.TrimSpace(os.Getenv("TABLEKIT_ENCRYPTION_KEY"))
	if rawKey == "" {
		return nil, errors.New("TABLEKIT_ENCRYPTION_KEY is required (generate with: openssl rand -base64 32)")
	}
	key, err := base64.StdEncoding.DecodeString(rawKey)
	if err != nil {
		return nil, fmt.Errorf("TABLEKIT_ENCRYPTION_KEY must be base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("TABLEKIT_ENCRYPTION_KEY must decode to 32 bytes, got %d", len(key))
	}
	c.EncryptionKey = key

	origins := strings.TrimSpace(os.Getenv("TABLEKIT_WEBAUTHN_ORIGINS"))
	if origins == "" {
		origins = c.PublicScheme + "://" + c.BaseDomain + ",http://localhost:8080"
	}
	for _, origin := range strings.Split(origins, ",") {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("invalid WebAuthn origin %q", origin)
		}
		c.WebAuthnOrigins = append(c.WebAuthnOrigins, strings.TrimSuffix(origin, "/"))
	}
	if len(c.WebAuthnOrigins) == 0 {
		return nil, errors.New("TABLEKIT_WEBAUTHN_ORIGINS must contain at least one origin")
	}
	if c.WebAuthnRPID == "" {
		return nil, errors.New("TABLEKIT_WEBAUTHN_RP_ID is required")
	}
	if c.IsProduction() && c.DatabaseURL == c.MigrateDatabaseURL && c.MigrateDatabaseURL != "" {
		return nil, errors.New("application and migration database roles must differ in production")
	}
	if c.IsProduction() && !c.CookieSecure {
		return nil, errors.New("TABLEKIT_COOKIE_SECURE must be true in production")
	}

	return c, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func intEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func boolEnv(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}
