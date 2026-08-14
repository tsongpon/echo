package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"

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
		log.Println("no .env file found; relying on environment variables")
	}

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
			log.Fatalf("failed to generate jwt secret: %v", err)
		}
		secret = hex.EncodeToString(rb)
		log.Println("WARNING: JWT_SECRET not set; using a random secret that will not persist across restarts")
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
		log.Fatalf("failed to create token signer: %v", err)
	}
	verifySigner, err := auth.NewEmailVerificationTokenSigner(secret, auth.DefaultVerificationTTL)
	if err != nil {
		log.Fatalf("failed to create verification token signer: %v", err)
	}
	employeeService := service.NewEmployeeService(employeeRepo, mailer.NewLogMailer(""), verifySigner)
	employeeHandler := handler.NewEmployeeHandler(employeeService, tokenSigner)

	e.GET("/ping", func(c *echo.Context) error {
		return c.String(http.StatusOK, "pong")
	})
	e.POST("/v1/register", employeeHandler.Register)
	e.POST("/v1/login", employeeHandler.Login)
	e.POST("/v1/verify-email", employeeHandler.VerifyEmail)
	e.GET("/v1/me", employeeHandler.Me, handler.Auth(tokenSigner))

	if err := e.Start(":" + port); err != nil {
		e.Logger.Error("failed to start server", "error", err)
	}
}
