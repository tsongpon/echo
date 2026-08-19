# API Test Commands

Test commands for every endpoint exposed by the Echo API. All request bodies
are JSON; responses are JSON. The dev server listens on `http://localhost:1323`.

| Method | Path                | Auth           | Purpose                                                       |
|--------|---------------------|----------------|---------------------------------------------------------------|
| GET    | `/ping`             | —              | Liveness check.                                               |
| GET    | `/v1/openapi.yaml`  | —              | The OpenAPI 3.1 specification (YAML).                         |
| GET    | `/v1/docs`          | —              | Interactive Swagger UI (loads `/v1/openapi.yaml`).            |
| POST   | `/v1/register`      | —              | Create a new employee (sends a verification email).           |
| GET    | `/v1/verify-email`  | —              | Verify an email using the token from the verification email.  |
| POST   | `/v1/login`         | —              | Authenticate and obtain a JWT.                                |
| GET    | `/v1/me`            | Bearer token   | Get the authenticated employee's profile.                     |
| GET    | `/v1/employees`     | Bearer token   | List the employees in the caller's organization.             |
| POST   | `/v1/invitation`    | Bearer token¹  | Issue an invitation token (org admins only).                  |
| POST   | `/v1/feedback-periods` | Bearer token¹ | Open a feedback period for the caller's organization (org admins only). |
| GET    | `/v1/feedback-periods` | Bearer token | List the feedback periods for the caller's organization.        |
| POST   | `/v1/feedbacks`      | Bearer token   | File a feedback entry for a colleague.                        |

¹ The caller's JWT `role` claim must be `org_admin`; any other role gets `403`.

---

## Ping

`GET /ping` — liveness check, no auth.

```bash
curl -s -w "\nHTTP %{http_code}\n" http://localhost:1323/ping
```

Expected response: `HTTP 200` with body `pong`.

---

## OpenAPI Specification & Swagger UI

The API is documented as an OpenAPI 3.1 document, embedded in the binary and
served at two public endpoints.

### Raw spec

`GET /v1/openapi.yaml` — the OpenAPI 3.1 document as YAML. No auth. Feed this
URL to any OpenAPI-compatible tool (Swagger UI, Postman, code generators, etc.).

```bash
curl -s http://localhost:1323/v1/openapi.yaml
```

The same file is the source of truth at `cmd/server/openapi.yaml` in the repo;
it is embedded into the binary at build time via `go:embed`, so deployments do
not ship a separate file. CI lints it with Spectral (see
`.github/workflows/ci.yml`).

### Interactive docs

`GET /v1/docs` — a Swagger UI page that loads `/v1/openapi.yaml`. No auth. The
page is a single static HTML document that pulls `swagger-ui-bundle.js` from a
public CDN; there is no Go-side dependency or asset pipeline.

Open `http://localhost:1323/v1/docs` in a browser to explore and try the
endpoints interactively.

| Endpoint             | Auth | Purpose                                  |
|----------------------|------|------------------------------------------|
| `GET /v1/openapi.yaml` | —  | Raw OpenAPI 3.1 spec (YAML).            |
| `GET /v1/docs`         | —  | Swagger UI (loads the spec).             |

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

## List Employees

`GET /v1/employees` — returns one page of employees in the authenticated
caller's organization, ordered by name ascending. Requires a valid `Bearer`
JWT; any authenticated employee may list colleagues (an employee needs to see
colleagues to file feedback against them). The organization is taken from the
caller's JWT, so an employee can only see their own organization's members. The
response omits the `password` field.

Pagination is cursor-based and controlled by two optional query parameters:

| Parameter | Default | Notes                                                                                  |
|-----------|---------|----------------------------------------------------------------------------------------|
| `limit`   | `20`    | Page size. Non-numeric or `<= 0` falls back to the default; values above `100` are capped. |
| `cursor`  | —       | The `next_cursor` value from the previous page (an employee ID). Omit on the first page. An unknown cursor returns `400`. |

```bash
curl -s -w "\nHTTP %{http_code}\n" -X GET "http://localhost:1323/v1/employees?limit=20&cursor=0190bbbb-..." \
  -H "Authorization: Bearer <access_token>"
```

Expected response: `HTTP 200`:

```json
{
  "employees": [
    {
      "id": "0190aaaa-...",
      "name": "Alice",
      "organization_name": "Acme",
      "role": "org_admin",
      "manager_id": null,
      "title": "Senior Engineer",
      "email": "alice@example.com",
      "is_mail_verified": true,
      "created_at": "2026-08-18T10:00:00Z",
      "updated_at": "2026-08-18T10:00:00Z"
    }
  ],
  "next_cursor": "0190aaaa-..."
}
```

`next_cursor` is the ID of the last employee on this page; pass it as `cursor`
on the next request to fetch the following page. When there are no more pages
`next_cursor` is `null`:

```json
{ "employees": [], "next_cursor": null }
```

An organization with no other members returns `HTTP 200` with
`{"employees": [], "next_cursor": null}`. The caller is included in the list;
the frontend should filter the caller out when populating a reviewee picker
(the backend enforces no self-review at `POST /v1/feedbacks`).

| Status | `message`                                       | When                                                          |
|--------|-------------------------------------------------|---------------------------------------------------------------|
| 400    | `"unknown cursor"`                              | `cursor` does not refer to an existing employee.              |
| 401    | `"missing or invalid token"`                    | No/invalid `Authorization` header or bad token.               |
| 500    | `"failed to list employees"`                    | Unexpected server error.                                      |

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

---

## Create Feedback Period

`POST /v1/feedback-periods` — opens a feedback period for the authenticated
employee's organization. Requires a valid `Bearer` JWT and the caller must have
`role: "org_admin"`; any other role returns `403`. The `organization_name` is
taken from the caller's JWT (not the body), so a client cannot open a period for
an org they do not belong to.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X POST http://localhost:1323/v1/feedback-periods \
  -H "Authorization: Bearer <access_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "H2 2026",
    "start_date": "2026-07-01T00:00:00Z",
    "end_date": "2026-12-31T23:59:59Z"
  }'
```

| Field         | Required | Notes                                                                                  |
|---------------|----------|----------------------------------------------------------------------------------------|
| `name`        | yes      | Trimmed; must not be empty.                                                            |
| `start_date`  | yes      | ISO 8601 timestamp; must not be the zero time.                                          |
| `end_date`    | yes      | ISO 8601 timestamp; must not be the zero time and must be after `start_date`.           |

Expected response: `HTTP 201`:

```json
{
  "id": "0190abcd-...",
  "name": "H2 2026",
  "organization_name": "Acme",
  "start_date": "2026-07-01T00:00:00Z",
  "end_date": "2026-12-31T23:59:59Z",
  "created_at": "2026-08-18T10:00:00Z",
  "updated_at": "2026-08-18T10:00:00Z"
}
```

| Status | `message`                                       | When                                                          |
|--------|-------------------------------------------------|---------------------------------------------------------------|
| 400    | `"invalid request body"`                        | Malformed/non-JSON body.                                      |
| 400    | `"<validation message>"`                        | Missing `name`, missing/invalid `start_date` or `end_date`, or `end_date` not after `start_date`. |
| 401    | `"missing or invalid token"`                    | No/invalid `Authorization` header or bad token.               |
| 403    | `"only org admins can create feedback periods"` | Caller's `role` is not `org_admin`.                           |
| 500    | `"failed to create feedback period"`            | Unexpected server error.                                      |

---

## List Feedback Periods

`GET /v1/feedback-periods` — returns the feedback periods for the authenticated
employee's organization, ordered by start date descending (most recent first).
Requires a valid `Bearer` JWT; any authenticated employee may list periods (an
employee needs to see periods in order to file feedback against them). The
organization is taken from the caller's JWT, so an employee can only see their
own organization's periods.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X GET http://localhost:1323/v1/feedback-periods \
  -H "Authorization: Bearer <access_token>"
```

Expected response: `HTTP 200`:

```json
{
  "periods": [
    {
      "id": "0190abcd-...",
      "name": "H2 2026",
      "organization_name": "Acme",
      "start_date": "2026-07-01T00:00:00Z",
      "end_date": "2026-12-31T23:59:59Z",
      "created_at": "2026-08-18T10:00:00Z",
      "updated_at": "2026-08-18T10:00:00Z"
    },
    {
      "id": "0190abce-...",
      "name": "H1 2026",
      "organization_name": "Acme",
      "start_date": "2026-01-01T00:00:00Z",
      "end_date": "2026-06-30T23:59:59Z",
      "created_at": "2026-02-01T10:00:00Z",
      "updated_at": "2026-02-01T10:00:00Z"
    }
  ]
}
```

An organization with no periods yet returns `HTTP 200` with `{"periods": []}`.

| Status | `message`                                       | When                                                          |
|--------|-------------------------------------------------|---------------------------------------------------------------|
| 401    | `"missing or invalid token"`                    | No/invalid `Authorization` header or bad token.               |
| 500    | `"failed to list feedback periods"`             | Unexpected server error.                                      |

---

## Create Feedback

`POST /v1/feedbacks` — files a feedback entry for a colleague. Requires a valid
`Bearer` JWT; any authenticated employee may file feedback. The reviewer is the
authenticated employee (taken from the JWT subject), so `reviewer_id` in the body
is ignored. A reviewer cannot review themselves.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X POST http://localhost:1323/v1/feedbacks \
  -H "Authorization: Bearer <access_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "period_id": "0190abcd-...",
    "reviewee_id": "<colleague employee id>",
    "communication_score": 4,
    "leadership_score": 5,
    "technical_score": 3,
    "collaboration_score": 4,
    "delivery_score": 5,
    "trust_score": 2,
    "strengths_comment": "great teammate",
    "weaknesses_comment": "could document more",
    "visibility": "anonymous"
  }'
```

| Field                | Required | Notes                                                                                  |
|----------------------|----------|----------------------------------------------------------------------------------------|
| `period_id`          | yes      | Trimmed; must not be empty.                                                            |
| `reviewee_id`        | yes      | Trimmed; must not be empty. Must differ from the reviewer (no self-review).            |
| `communication_score`| yes      | Integer 1–5.                                                                          |
| `leadership_score`   | yes      | Integer 1–5.                                                                            |
| `technical_score`    | yes      | Integer 1–5.                                                                            |
| `collaboration_score`| yes      | Integer 1–5.                                                                           |
| `delivery_score`     | yes      | Integer 1–5.                                                                            |
| `trust_score`        | yes      | Integer 1–5.                                                                            |
| `strengths_comment`  | yes      | Free text; must not be empty.                                                          |
| `weaknesses_comment` | yes      | Free text; must not be empty.                                                         |
| `visibility`         | no       | One of `"anonymous"`, `"named"`. Defaults to `"anonymous"` when omitted or empty. |

Expected response: `HTTP 201`:

```json
{
  "id": "0190abcd-...",
  "period_id": "0190abcd-...",
  "reviewee_id": "<colleague employee id>",
  "reviewer_id": "<authenticated employee id>",
  "communication_score": 4,
  "leadership_score": 5,
  "technical_score": 3,
  "collaboration_score": 4,
  "delivery_score": 5,
  "trust_score": 2,
  "strengths_comment": "great teammate",
  "weaknesses_comment": "could document more",
  "visibility": "anonymous",
  "created_at": "2026-08-18T10:00:00Z",
  "updated_at": "2026-08-18T10:00:00Z"
}
```

| Status | `message`                                       | When                                                          |
|--------|-------------------------------------------------|---------------------------------------------------------------|
| 400    | `"invalid request body"`                        | Malformed/non-JSON body.                                      |
| 400    | `"<validation message>"`                        | Missing `period_id`/`reviewee_id`, self-review, a score outside 1–5, or an unknown `visibility`. |
| 401    | `"missing or invalid token"`                    | No/invalid `Authorization` header or bad token.               |
| 500    | `"failed to create feedback"`                   | Unexpected server error.                                      |