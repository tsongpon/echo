package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"cloud.google.com/go/firestore"
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
	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found; relying on environment variables")
	}

	configureLogger()

	e := echo.New()

	e.Use(middleware.RequestLogger())
	e.Use(middleware.Recover())

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
	employeeRepo, repoCloser, err := buildEmployeeRepository(context.Background())
	if err != nil {
		fatal("failed to create employee repository", "error", err)
	}
	if repoCloser != nil {
		defer repoCloser()
	}
	tokenSigner, err := auth.NewTokenSigner(secret, auth.DefaultTTL)
	if err != nil {
		fatal("failed to create token signer", "error", err)
	}
	verifySigner, err := auth.NewEmailVerificationTokenSigner(secret, auth.DefaultVerificationTTL)
	if err != nil {
		fatal("failed to create verification token signer", "error", err)
	}
	employeeService := service.NewEmployeeService(employeeRepo, buildMailer(), verifySigner, nil)
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

func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func buildEmployeeRepository(ctx context.Context) (service.EmployeeRepository, func(), error) {
	projectID := os.Getenv("FIRESTORE_PROJECT_ID")
	if projectID == "" {
		return nil, nil, errors.New("FIRESTORE_PROJECT_ID must be set")
	}

	client, err := firestore.NewClientWithDatabase(ctx, projectID, os.Getenv("FIRESTORE_DATABASE_NAME"))
	if err != nil {
		return nil, nil, err
	}
	slog.Info("using Firestore employee repository", "project", projectID)
	return repository.NewEmployeeFirestoreRepository(client, nil), func() {
		if err := client.Close(); err != nil {
			slog.Error("firestore: client close failed", "project", projectID, "error", err)
		}
	}, nil
}

func buildMailer() service.Mailer {
	if apiKey := os.Getenv("RESEND_API_KEY"); apiKey != "" {
		return mailer.NewResendMailer(apiKey, os.Getenv("RESEND_FROM_EMAIL"), os.Getenv("APP_BASE_URL"), nil)
	}
	slog.Info("RESEND_API_KEY not set; using LogMailer (verification links will be logged only)")
	return mailer.NewLogMailer(os.Getenv("APP_BASE_URL"), nil)
}
