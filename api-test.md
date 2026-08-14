# API Test Commands

## Register Employee

`POST /v1/register` — registers a new employee and returns the created record.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X POST http://localhost:1323/v1/register \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Alice",
    "organization_id": "org-1",
    "manager_id": null,
    "title": "Senior Engineer",
    "email": "alice@example.com",
    "password": "supersecret"
  }'
```

Expected response: `HTTP 201` with the created employee JSON (the `password`
field is omitted from the response). The response now also includes an
`is_mail_verified` field, which is `false` on a freshly registered account.

On success the server "sends" a verification email. With the default `LogMailer`
(no SMTP configured), the verification link is logged to the server's stdout:

```
verification email -> alice@example.com: http://localhost:1323/v1/verify-email?token=<token>
```

Use the `token` from that link with `POST /v1/verify-email` (below) to verify
the address.

## Verify Email

`GET /v1/verify-email` — marks the authenticated employee's email as verified
using a token issued during registration. The endpoint is public and is designed
to be the target of the verification link sent in the registration email, so the
token is supplied as the `token` query parameter. Identity is established solely
from the token, which is bound to a specific employee ID and expires after 24
hours.

```bash
curl -s -w "\nHTTP %{http_code}\n" "http://localhost:1323/v1/verify-email?token=<token>"
```

Expected response: `HTTP 200` with `{"message":"email verified"}`.

A missing, malformed, expired, or wrong-purpose token returns `HTTP 400` with
`{"message":"invalid or expired verification token"}`. Verification is
idempotent: verifying an already-verified email succeeds again with the same
`HTTP 200` response.

## Login

`POST /v1/login` — authenticates an employee by email and password and returns
a signed JWT on success.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X POST http://localhost:1323/v1/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "alice@example.com",
    "password": "supersecret"
  }'
```

Expected response: `HTTP 200` with an `access_token` (JWT, HS256),
`token_type: Bearer`, `expires_in` seconds, and the authenticated `employee`.

Invalid email or wrong password returns `HTTP 401` with
`{"message":"invalid email or password"}`.

If the credentials are valid but the employee's email has not been verified yet,
login returns `HTTP 403` with `{"message":"email not verified"}`. Verify the
email via `POST /v1/verify-email` (above) first, then retry login.

## Get Current Employee Profile

`GET /v1/me` — returns the profile of the authenticated employee. Requires a
valid `Bearer` JWT obtained from `POST /v1/login`; the employee is looked up
by the token's subject (the employee ID).

```bash
curl -s -w "\nHTTP %{http_code}\n" -X GET http://localhost:1323/v1/me \
  -H "Authorization: Bearer <access_token>"
```

Expected response: `HTTP 200` with the employee JSON (the `password` field is
omitted).

A missing or invalid `Authorization` header returns `HTTP 401` with
`{"message":"missing or invalid token"}`.

A token whose subject no longer matches a stored employee returns `HTTP 404`
with `{"message":"employee not found"}`.