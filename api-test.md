# API Test Commands

Test commands for every endpoint exposed by the Echo API. All request bodies
are JSON; responses are JSON. The dev server listens on `http://localhost:1323`.

| Method | Path                | Auth           | Purpose                                                       |
|--------|---------------------|----------------|---------------------------------------------------------------|
| GET    | `/ping`             | —              | Liveness check.                                               |
| POST   | `/v1/register`      | —              | Create a new employee (sends a verification email).           |
| GET    | `/v1/verify-email`  | —              | Verify an email using the token from the verification email.  |
| POST   | `/v1/login`         | —              | Authenticate and obtain a JWT.                                |
| GET    | `/v1/me`            | Bearer token   | Get the authenticated employee's profile.                     |
| POST   | `/v1/invitation`    | Bearer token¹  | Issue an invitation token (org admins only).                  |

¹ The caller's JWT `role` claim must be `org_admin`; any other role gets `403`.

---

## Ping

`GET /ping` — liveness check, no auth.

```bash
curl -s -w "\nHTTP %{http_code}\n" http://localhost:1323/ping
```

Expected response: `HTTP 200` with body `pong`.

---

## Register Employee

`POST /v1/register` — creates a new employee and returns the created record.
The `password` is hashed server-side with bcrypt and never returned.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X POST http://localhost:1323/v1/register \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Alice",
    "organization_name": "Acme",
    "manager_id": null,
    "title": "Senior Engineer",
    "email": "alice@example.com",
    "password": "supersecret",
    "invite_token": ""
  }'
```

| Field              | Required | Notes                                                                                  |
|--------------------|----------|----------------------------------------------------------------------------------------|
| `name`             | yes      | Trimmed; must not be empty.                                                            |
| `organization_name`| yes      | Trimmed; must not be empty.                                                            |
| `role`             | no       | Omitted/ignored on input — the server assigns it from `invite_token` (see below).      |
| `manager_id`       | no       | Employee ID of the user's manager, or `null`.                                          |
| `title`            | no       | Job title. Defaults to `""`.                                                           |
| `email`            | yes      | Stored lowercased; uniqueness is global across organizations.                          |
| `password`         | yes      | Plaintext; max 64 characters.                                                          |
| `invite_token`     | no       | If empty, the employee is created with `role: "org_admin"` (the first admin). If a valid invitation token is supplied, `role: "user"`. |

Expected response: `HTTP 201` with the created employee JSON. The `password`
field is omitted. A fresh account has `is_mail_verified: false` and
`role: "org_admin"` when registered without an invitation.

On success the server "sends" a verification email. With the default
`LogMailer` (no SMTP configured), the verification link is logged to the
server's stdout:

```
verification email -> alice@example.com: http://localhost:1323/v1/verify-email?token=<token>
```

Use the `token` from that link with `GET /v1/verify-email` (below) to verify
the address.

| Status | `message`                          | When                                                        |
|--------|------------------------------------|-------------------------------------------------------------|
| 400    | `"invalid request body"`           | Malformed/non-JSON body.                                    |
| 400    | `"<validation message>"`           | Missing/invalid fields (e.g. `"name is required"`, `"organization_name is required"`, `"password must be at most 64 characters"`). |
| 409    | `"email already taken"`            | An employee with that email already exists.                 |
| 500    | `"failed to register employee"`    | Unexpected server error.                                    |

---

## Verify Email

`GET /v1/verify-email` — validates the email-verification token and marks the
matching employee's email as verified. The endpoint is public and is designed
to be the target of the verification link sent in the registration email, so
the token is supplied as the `token` query parameter. Identity is established
solely from the token, which is bound to a specific employee ID and expires
after 24 hours.

```bash
curl -s -w "\nHTTP %{http_code}\n" "http://localhost:1323/v1/verify-email?token=<token>"
```

Expected response: `HTTP 200` with `{"message":"email verified"}`.

A missing, malformed, expired, or wrong-purpose token returns `HTTP 400` with
`{"message":"invalid or expired verification token"}`. Verification is
idempotent: verifying an already-verified email succeeds again with the same
`HTTP 200` response.

---

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
`token_type: "Bearer"`, `expires_in` seconds, and the authenticated `employee`.

Invalid email or wrong password returns `HTTP 401` with
`{"message":"invalid email or password"}`.

If the credentials are valid but the employee's email has not been verified yet,
login returns `HTTP 403` with `{"message":"email not verified"}`. Verify the
email via `GET /v1/verify-email` (above) first, then retry login.

---

## Get Current Employee Profile

`GET /v1/me` — returns the profile of the authenticated employee. Requires a
valid `Bearer` JWT obtained from `POST /v1/login`; the employee is looked up
by the token's subject (the employee ID).

```bash
curl -s -w "\nHTTP %{http_code}\n" -X GET http://localhost:1323/v1/me \
  -H "Authorization: Bearer <access_token>"
```

Expected response: `HTTP 200` with the employee JSON (the `password` field is
omitted). The response includes `role` (`"org_admin"` or `"user"`) and
`organization_name`.

A missing or invalid `Authorization` header returns `HTTP 401` with
`{"message":"missing or invalid token"}`.

A token whose subject no longer matches a stored employee returns `HTTP 404`
with `{"message":"employee not found"}`.

---

## Create Invitation

`POST /v1/invitation` — issues a signed invitation token that lets the bearer
register as a member of the named organization. Requires a valid `Bearer` JWT
and the caller must have `role: "org_admin"`; any other role returns `403`.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X POST http://localhost:1323/v1/invitation \
  -H "Authorization: Bearer <access_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "organization_name": "Acme"
  }'
```

| Field              | Required | Notes                                                                                  |
|--------------------|----------|----------------------------------------------------------------------------------------|
| `organization_name`| yes      | The organization the invitee will join.                                                |
| `expires_at`       | no       | ISO 8601 timestamp overriding the default 7-day lifetime. If omitted the token expires after 7 days. Must be in the future. |

Expected response: `HTTP 201`:

```json
{
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "id": "0190abcd-...",
  "created_by": "<inviter employee id>",
  "organization_name": "Acme",
  "created_at": "2026-08-18T10:00:00Z",
  "expires_at": "2026-08-25T10:00:00Z"
}
```

The returned `token` is then passed as `invite_token` in `POST /v1/register`
to create the invitee's account with `role: "user"`.

| Status | `message`                                       | When                                                          |
|--------|-------------------------------------------------|---------------------------------------------------------------|
| 400    | `"invalid request body"`                        | Malformed/non-JSON body.                                      |
| 400    | `"<validation message>"`                        | Missing `organization_name` or `expires_at` not in the future. |
| 401    | `"missing or invalid token"`                    | No/invalid `Authorization` header or bad token.               |
| 403    | `"only org admins can create invitations"`      | Caller's `role` is not `org_admin`.                           |
| 500    | `"failed to create invitation"`                 | Unexpected server error.                                      |