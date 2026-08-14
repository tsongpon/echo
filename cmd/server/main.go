package main

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/tsongpon/echo/internal/auth"
	"github.com/tsongpon/echo/internal/handler"
	"github.com/tsongpon/echo/internal/mailer"
	"github.com/tsongpon/echo/internal/repository"
	"github.com/tsongpon/echo/internal/service"
)

func main() {
	// Load .env if present. Existing real environment variables take
	// precedence over values in the file, so this is safe to call in any
	// environment.
	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found; relying on environment variables")
	}

	configureLogger()

	e := echo.New()

	e.Use(middleware.RequestLogger())
	e.Use(middleware.Recover())

	// JWT signing secret. Prefer the JWT_SECRET env var in production; fall
	// back to a random secret (suitable only for local dev, since tokens
	// won't survive a restart).
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		rb := make([]byte, 32)
		if _, err := rand.Read(rb); err != nil {
			fatal("failed to generate jwt secret", "error", err)
		}
		secret = hex.EncodeToString(rb)
		slog.Warn("JWT_SECRET not set; using a random secret that will not persist across restarts")
	}

	// Listen port. Defaults to 1323.
	port := os.Getenv("PORT")
	if port == "" {
		port = "1323"
	}

	// Wire up dependencies: repository -> service -> handler.
	employeeRepo := repository.NewEmployeeInMemoryRepository()
	tokenSigner, err := auth.NewTokenSigner(secret, auth.DefaultTTL)
	if err != nil {
		fatal("failed to create token signer", "error", err)
	}
	verifySigner, err := auth.NewEmailVerificationTokenSigner(secret, auth.DefaultVerificationTTL)
	if err != nil {
		fatal("failed to create verification token signer", "error", err)
	}
	employeeService := service.NewEmployeeService(employeeRepo, mailer.NewLogMailer("", nil), verifySigner, nil)
	employeeHandler := handler.NewEmployeeHandler(employeeService, tokenSigner)

	e.GET("/ping", func(c *echo.Context) error {
		return c.String(http.StatusOK, "pong")
	})
	e.POST("/v1/register", employeeHandler.Register)
	e.POST("/v1/login", employeeHandler.Login)
	e.GET("/v1/verify-email", employeeHandler.VerifyEmail)
	e.GET("/v1/me", employeeHandler.Me, handler.Auth(tokenSigner))

	if err := e.Start(":" + port); err != nil {
		slog.Error("failed to start server", "error", err)
	}
}

// configureLogger installs a slog text handler writing to os.Stdout as the
// default logger. The level is taken from the LOG_LEVEL env var (debug, info,
// warn, error) and defaults to info.
func configureLogger() {
	var level slog.Level
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}

// fatal logs an error at the error level and exits with status 1. It is the
// slog equivalent of log.Fatalf for startup failures that cannot be recovered.
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}
