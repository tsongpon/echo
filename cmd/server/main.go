package main

import (
	"context"
	"crypto/rand"
	_ "embed"
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

// openapiSpec is the embedded OpenAPI 3.1 document served at /v1/openapi.yaml.
// Using go:embed keeps the spec in the binary so deployments don't ship a
// separate file; the spec stays the source of truth at repo root.
//
//go:embed openapi.yaml
var openapiSpec []byte

// swaggerUIPage is a minimal Swagger UI page that loads the spec from
// /v1/openapi.yaml. The HTML is static and pulls swagger-ui-bundle.js from a
// public CDN, so no Go dependency or asset pipeline is needed.
const swaggerUIPage = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>Echo API Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: "/v1/openapi.yaml",
        dom_id: "#swagger-ui",
        deepLinking: true,
        presets: [SwaggerUIBundle.presets.apis],
        layout: "BaseLayout",
      });
    };
  </script>
</body>
</html>`

func main() {
	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found; relying on environment variables")
	}

	configureLogger()

	e := echo.New()

	e.Use(middleware.RequestLogger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORSWithConfig(buildCORSConfig()))

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

	// Wire up dependencies: repository -> service -> handler. The Firestore
	// client is shared across repositories (it is per-project/database, not
	// per-collection), so it is built once and closed via a single closer.
	firestoreClient, repoCloser, err := buildFirestoreClient(context.Background())
	if err != nil {
		fatal("failed to create firestore client", "error", err)
	}
	if repoCloser != nil {
		defer repoCloser()
	}
	employeeRepo := repository.NewEmployeeFirestoreRepository(firestoreClient, nil)
	tokenSigner, err := auth.NewTokenSigner(secret, auth.DefaultTTL)
	if err != nil {
		fatal("failed to create token signer", "error", err)
	}
	verifySigner, err := auth.NewEmailVerificationTokenSigner(secret, auth.DefaultVerificationTTL)
	if err != nil {
		fatal("failed to create verification token signer", "error", err)
	}
	invitationSigner, err := auth.NewInvitationTokenSigner(secret, auth.DefaultInvitationTTL)
	if err != nil {
		fatal("failed to create invitation token signer", "error", err)
	}
	employeeService := service.NewEmployeeService(employeeRepo, buildMailer(), verifySigner, invitationSigner, nil)
	employeeHandler := handler.NewEmployeeHandler(employeeService, tokenSigner, nil)
	invitationService := service.NewInvitationService(invitationSigner, nil)
	invitationHandler := handler.NewInvitationHandler(invitationService)
	feedbackPeriodRepo := repository.NewFeedbackPeriodFirestoreRepository(firestoreClient, nil)
	feedbackPeriodService := service.NewFeedbackPeriodService(feedbackPeriodRepo, nil)
	feedbackPeriodHandler := handler.NewFeedbackPeriodHandler(feedbackPeriodService, nil)
	feedbackRepo := repository.NewFeedbackFirestoreRepository(firestoreClient, nil)
	feedbackService := service.NewFeedbackService(feedbackRepo, feedbackPeriodRepo, nil)
	feedbackHandler := handler.NewFeedbackHandler(feedbackService, nil)

	e.GET("/ping", func(c *echo.Context) error {
		return c.String(http.StatusOK, "pong")
	})
	e.GET("/v1/openapi.yaml", func(c *echo.Context) error {
		return c.Blob(http.StatusOK, "text/yaml; charset=utf-8", openapiSpec)
	})
	e.GET("/v1/docs", func(c *echo.Context) error {
		return c.HTML(http.StatusOK, swaggerUIPage)
	})
	e.POST("/v1/register", employeeHandler.Register)
	e.POST("/v1/login", employeeHandler.Login)
	e.GET("/v1/verify-email", employeeHandler.VerifyEmail)
	e.GET("/v1/me", employeeHandler.Me, handler.Auth(tokenSigner))
	e.GET("/v1/employees", employeeHandler.ListEmployees, handler.Auth(tokenSigner))
	e.POST("/v1/invitation", invitationHandler.CreateInvitation, handler.Auth(tokenSigner))
	e.POST("/v1/feedback-periods", feedbackPeriodHandler.CreateFeedbackPeriod, handler.Auth(tokenSigner))
	e.GET("/v1/feedback-periods", feedbackPeriodHandler.ListFeedbackPeriods, handler.Auth(tokenSigner))
	e.POST("/v1/feedbacks", feedbackHandler.CreateFeedback, handler.Auth(tokenSigner))

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

func buildFirestoreClient(ctx context.Context) (*firestore.Client, func(), error) {
	projectID := os.Getenv("FIRESTORE_PROJECT_ID")
	if projectID == "" {
		return nil, nil, errors.New("FIRESTORE_PROJECT_ID must be set")
	}

	client, err := firestore.NewClientWithDatabase(ctx, projectID, os.Getenv("FIRESTORE_DATABASE_NAME"))
	if err != nil {
		return nil, nil, err
	}
	slog.Info("using Firestore", "project", projectID)
	return client, func() {
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

// buildCORSConfig configures the CORS middleware to allow all origins. The
// current auth scheme is a bearer token in the Authorization header rather
// than cookies, so a wildcard origin is safe and AllowCredentials is left
// false -- browsers reject Access-Control-Allow-Credentials together with a
// wildcard origin anyway, and the API does not rely on credentialed requests.
//
// Allowed headers cover Authorization (bearer token) and Content-Type (JSON
// request bodies); allowed methods cover the full CRUD set used by the API.
// Preflight responses are cached for 5 minutes to keep OPTIONS traffic down.
func buildCORSConfig() middleware.CORSConfig {
	return middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowHeaders: []string{echo.HeaderAuthorization, echo.HeaderContentType, echo.HeaderAccept},
		AllowMethods: []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete},
		MaxAge:       300,
	}
}
