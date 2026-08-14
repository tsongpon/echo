package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/tsongpon/echo/internal/auth"
)

// contextKeyUser is the key under which Auth stores the verified *auth.Claims
// in the echo context.
const contextKeyUser = "auth.user"

// Auth returns an Echo middleware that requires a valid Bearer JWT. On
// success the verified *auth.Claims are stored in the request context and
// can be retrieved with ClaimsFromContext. A missing or invalid token yields
// 401 with a generic message.
func Auth(signer *auth.TokenSigner) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			header := c.Request().Header.Get("Authorization")
			token, ok := bearerToken(header)
			if !ok {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
			}

			claims, err := signer.Verify(token)
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
			}

			c.Set(contextKeyUser, claims)
			return next(c)
		}
	}
}

// ClaimsFromContext returns the verified claims stored by Auth, or nil if the
// request was not authenticated. A nil return is a programming error on a
// protected route and should be treated as unauthorized by the caller.
func ClaimsFromContext(c *echo.Context) *auth.Claims {
	v := c.Get(contextKeyUser)
	if v == nil {
		return nil
	}
	claims, _ := v.(*auth.Claims)
	return claims
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header. Comparison of the scheme is case-insensitive per RFC 7235; the
// returned token is trimmed of surrounding whitespace.
func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
