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
| GET    | `/v1/me/feedbacks`   | Bearer token   | List feedback received by the current employee (paginated).   |
| GET    | `/v1/me/given-feedbacks` | Bearer token | List feedback submitted by the current employee (paginated). |
| POST   | `/v1/feedback-drafts` | Bearer token | Start a feedback draft (at most one per reviewee/period).     |
| GET    | `/v1/feedback-drafts` | Bearer token | List the caller's feedback drafts (paginated).                |
| GET    | `/v1/feedback-drafts/:id` | Bearer token | Get one of the caller's drafts.                         |
| PATCH  | `/v1/feedback-drafts/:id` | Bearer token | Partially update one of the caller's drafts.             |
| POST   | `/v1/feedback-drafts/:id/submit` | Bearer token | Submit one of the caller's drafts.                |
| DELETE | `/v1/feedback-drafts/:id` | Bearer token | Delete one of the caller's drafts.                       |
| GET    | `/v1/me/reports`    | Bearer token   | List the caller's direct reportees (paginated).              |
| PATCH  | `/v1/employees/:id/manager` | Bearer token¹ | Assign or clear an employee's manager (org admins only). |
| GET    | `/v1/employees/:id/feedbacks` | Bearer token | List feedback received by a reportee (manager view, paginated). |

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

The `period_id` must refer to an existing period **whose date window is open**:
now must fall within `[start_date, end_date]`. A not-yet-open or already-ended
period returns `422`.

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
  "status": "submitted",
  "created_at": "2026-08-18T10:00:00Z",
  "updated_at": "2026-08-18T10:00:00Z"
}
```

| Status | `message`                                       | When                                                          |
|--------|-------------------------------------------------|---------------------------------------------------------------|
| 400    | `"invalid request body"`                        | Malformed/non-JSON body.                                      |
| 400    | `"<validation message>"`                        | Missing `period_id`/`reviewee_id`, self-review, a score outside 1–5, or an unknown `visibility`. |
| 401    | `"missing or invalid token"`                    | No/invalid `Authorization` header or bad token.               |
| 422    | `"feedback period is not open for submission"`  | The period has not started or has already ended.             |
| 500    | `"failed to create feedback"`                   | Unexpected server error.                                      |

---

## List My Feedback

`GET /v1/me/feedbacks` — returns one page of feedback entries received by the
authenticated employee (i.e. entries others have written about them), ordered
by `created_at` descending (newest first). Requires a valid `Bearer` JWT; any
authenticated employee may list their own received feedback. The reviewee is
taken from the caller's JWT subject, so an employee can only list their own
received feedback.

Visibility policy: for entries with `visibility: "anonymous"`, the reviewer's
identity is hidden from the caller (the reviewee) — `reviewer_id` is empty in
the response. Entries with `visibility: "named"` include `reviewer_id` as usual.

Pagination is cursor-based and controlled by two optional query parameters:

| Parameter | Default | Notes                                                                                  |
|-----------|---------|----------------------------------------------------------------------------------------|
| `limit`   | `20`    | Page size. Non-numeric or `<= 0` falls back to the default; values above `100` are capped. |
| `cursor`  | —       | The `next_cursor` value from the previous page (a feedback ID). Omit on the first page. An unknown cursor returns `400`. |

```bash
curl -s -w "\nHTTP %{http_code}\n" -X GET "http://localhost:1323/v1/me/feedbacks?limit=20&cursor=0190bbbb-..." \
  -H "Authorization: Bearer <access_token>"
```

Expected response: `HTTP 200`:

```json
{
  "feedbacks": [
    {
      "id": "0190abcd-...",
      "period_id": "0190abca-...",
      "reviewee_id": "<authenticated employee id>",
      "reviewer_id": "",
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
  ],
  "next_cursor": "0190abcd-..."
}
```

`next_cursor` is the ID of the last feedback entry on this page; pass it as
`cursor` on the next request to fetch the following page. When there are no
more pages `next_cursor` is `null`:

```json
{ "feedbacks": [], "next_cursor": null }
```

An employee who has received no feedback returns `HTTP 200` with
`{"feedbacks": [], "next_cursor": null}`.

| Status | `message`                                       | When                                                          |
|--------|-------------------------------------------------|---------------------------------------------------------------|
| 400    | `"unknown cursor"`                              | `cursor` does not refer to an existing feedback entry.         |
| 401    | `"missing or invalid token"`                    | No/invalid `Authorization` header or bad token.               |
| 500    | `"failed to list feedbacks"`                    | Unexpected server error.                                     |
---

## List My Given Feedback

`GET /v1/me/given-feedbacks` — returns one page of feedback entries the
authenticated employee has submitted (i.e. entries they wrote as the
reviewer), ordered by `created_at` descending (newest first). Requires a
valid `Bearer` JWT. The reviewer is taken from the caller's JWT subject, so
an employee can only list the feedback they gave themselves. Only submitted
entries are listed; drafts stay in `GET /v1/feedback-drafts`.

Visibility policy: none applies here — the caller is the reviewer of every
entry, so `reviewer_id` is always included, even for anonymous entries (an
author always knows their own identity).

Pagination is cursor-based and controlled by the same two optional query
parameters as `GET /v1/me/feedbacks`:

| Parameter | Default | Notes                                                                                  |
|-----------|---------|----------------------------------------------------------------------------------------|
| `limit`   | `20`    | Page size. Non-numeric or `<= 0` falls back to the default; values above `100` are capped. |
| `cursor`  | —       | The `next_cursor` value from the previous page (a feedback ID). Omit on the first page. An unknown cursor returns `400`. |

```bash
curl -s -w "\nHTTP %{http_code}\n" -X GET "http://localhost:1323/v1/me/given-feedbacks?limit=20" \
  -H "Authorization: Bearer <access_token>"
```

Expected response: `HTTP 200`:

```json
{
  "feedbacks": [
    {
      "id": "0190abcd-...",
      "period_id": "0190abca-...",
      "reviewee_id": "0190bbbb-...",
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
      "status": "submitted",
      "created_at": "2026-08-18T10:00:00Z",
      "updated_at": "2026-08-18T10:00:00Z"
    }
  ],
  "next_cursor": null
}
```

An employee who has given no feedback returns `HTTP 200` with
`{"feedbacks": [], "next_cursor": null}`.

| Status | `message`                                       | When                                                          |
|--------|-------------------------------------------------|---------------------------------------------------------------|
| 400    | `"unknown cursor"`                              | `cursor` does not refer to an existing feedback entry.         |
| 401    | `"missing or invalid token"`                    | No/invalid `Authorization` header or bad token.               |
| 500    | `"failed to list feedbacks"`                    | Unexpected server error.                                     |
---

## Create Feedback Draft

`POST /v1/feedback-drafts` — starts a draft feedback entry owned by the
authenticated employee. Requires a valid `Bearer` JWT. Unlike
`POST /v1/feedbacks`, a draft requires only the target and period: scores,
comments, and visibility may be filled in later via `PATCH /v1/feedback-drafts/:id`.

At most one draft may exist per (reviewer, reviewee, period) triple; a
duplicate returns `409`. The period's date window is intentionally **not**
enforced here: a draft may be started before the period opens and will fail
at submit time if the window is still shut.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X POST http://localhost:1323/v1/feedback-drafts \
  -H "Authorization: Bearer <access_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "period_id": "0190abcd-...",
    "reviewee_id": "<colleague employee id>",
    "communication_score": 4,
    "strengths_comment": "work in progress",
    "visibility": "anonymous"
  }'
```

| Field                | Required | Notes                                                                                  |
|----------------------|----------|----------------------------------------------------------------------------------------|
| `period_id`          | yes      | Must refer to an existing feedback period. The period's date window is not enforced yet. |
| `reviewee_id`        | yes      | Must differ from the reviewer (no self-review).                                        |
| `communication_score`| no       | Integer 1–5 when supplied; may be filled in later.                                     |
| `leadership_score`   | no       | Integer 1–5 when supplied.                                                              |
| `technical_score`    | no       | Integer 1–5 when supplied.                                                              |
| `collaboration_score`| no       | Integer 1–5 when supplied.                                                              |
| `delivery_score`     | no       | Integer 1–5 when supplied.                                                              |
| `trust_score`        | no       | Integer 1–5 when supplied.                                                              |
| `strengths_comment`  | no       | Free text; may be empty.                                                               |
| `weaknesses_comment` | no       | Free text; may be empty.                                                               |
| `visibility`         | no       | One of `"anonymous"`, `"named"`. Defaults to `"anonymous"` when omitted or empty.      |

Expected response: `HTTP 201`:

```json
{
  "id": "0190abcf-...",
  "period_id": "0190abcd-...",
  "reviewee_id": "<colleague employee id>",
  "reviewer_id": "<authenticated employee id>",
  "communication_score": 4,
  "leadership_score": 0,
  "technical_score": 0,
  "collaboration_score": 0,
  "delivery_score": 0,
  "trust_score": 0,
  "strengths_comment": "work in progress",
  "weaknesses_comment": "",
  "visibility": "anonymous",
  "status": "draft",
  "created_at": "2026-08-18T10:00:00Z",
  "updated_at": "2026-08-18T10:00:00Z"
}
```

Unfilled scores are `0` on a draft; they must all be set (1–5) before
submission.

| Status | `message`                                             | When                                                  |
|--------|-------------------------------------------------------|-------------------------------------------------------|
| 400    | `"invalid request body"`                              | Malformed/non-JSON body.                              |
| 400    | `"<validation message>"`                              | Missing `period_id`/`reviewee_id`, self-review, a supplied score outside 1–5, or an unknown `visibility`. |
| 401    | `"missing or invalid token"`                          | No/invalid `Authorization` header or bad token.       |
| 409    | `"a draft for this reviewee and period already exists"` | The reviewer already has a draft for this pair.     |
| 500    | `"failed"`                                            | Unexpected server error.                              |

---

## List My Feedback Drafts

`GET /v1/feedback-drafts` — returns one page of the authenticated employee's
own draft entries, ordered by `created_at` descending (newest first). Only
drafts are listed; entries the caller has already submitted are not included.
No reviewer redaction applies: the author always sees their own `reviewer_id`.

Pagination is cursor-based and controlled by two optional query parameters:

| Parameter | Default | Notes                                                                                  |
|-----------|---------|----------------------------------------------------------------------------------------|
| `limit`   | `20`    | Page size. Non-numeric or `<= 0` falls back to the default; values above `100` are capped. |
| `cursor`  | —       | The `next_cursor` value from the previous page (a draft ID). Omit on the first page. An unknown cursor returns `400`. |

```bash
curl -s -w "\nHTTP %{http_code}\n" -X GET "http://localhost:1323/v1/feedback-drafts?limit=20" \
  -H "Authorization: Bearer <access_token>"
```

Expected response: `HTTP 200`:

```json
{
  "drafts": [
    {
      "id": "0190abcf-...",
      "period_id": "0190abcd-...",
      "reviewee_id": "<colleague employee id>",
      "reviewer_id": "<authenticated employee id>",
      "communication_score": 4,
      "leadership_score": 0,
      "technical_score": 0,
      "collaboration_score": 0,
      "delivery_score": 0,
      "trust_score": 0,
      "strengths_comment": "work in progress",
      "weaknesses_comment": "",
      "visibility": "anonymous",
      "status": "draft",
      "created_at": "2026-08-18T10:00:00Z",
      "updated_at": "2026-08-18T10:00:00Z"
    }
  ],
  "next_cursor": null
}
```

| Status | `message`                    | When                                            |
|--------|------------------------------|-------------------------------------------------|
| 400    | `"unknown cursor"`           | `cursor` does not refer to an existing draft.    |
| 401    | `"missing or invalid token"` | No/invalid `Authorization` header or bad token. |
| 500    | `"failed to list feedback drafts"` | Unexpected server error.                 |

---

## Get Feedback Draft

`GET /v1/feedback-drafts/:id` — returns the named draft when it belongs to the
authenticated caller. A draft owned by anyone else — like a draft that does not
exist — returns `404` rather than `403`, so a draft's existence never leaks to
other employees.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X GET "http://localhost:1323/v1/feedback-drafts/<draft id>" \
  -H "Authorization: Bearer <access_token>"
```

Expected response: `HTTP 200` with the draft JSON (same shape as create).

| Status | `message`                      | When                                            |
|--------|--------------------------------|-------------------------------------------------|
| 401    | `"missing or invalid token"`   | No/invalid `Authorization` header or bad token. |
| 404    | `"feedback draft not found"`   | No draft with this ID belongs to the caller.    |
| 500    | `"failed"`                     | Unexpected server error.                        |

---

## Update Feedback Draft

`PATCH /v1/feedback-drafts/:id` — applies a partial update to the caller's
draft. Only fields present in the body are overwritten; omitted fields keep
their stored values. `period_id`, `reviewee_id`, and `reviewer_id` are fixed
once the draft exists. A draft may stay incomplete; the full submit-time
validation runs at submit, not here.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X PATCH "http://localhost:1323/v1/feedback-drafts/<draft id>" \
  -H "Authorization: Bearer <access_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "communication_score": 4,
    "trust_score": 5,
    "strengths_comment": "great teammate"
  }'
```

| Field                | Required | Notes                                                        |
|----------------------|----------|--------------------------------------------------------------|
| `communication_score`| no       | Integer 1–5. Omitted → unchanged.                           |
| `leadership_score`   | no       | Integer 1–5. Omitted → unchanged.                            |
| `technical_score`    | no       | Integer 1–5. Omitted → unchanged.                           |
| `collaboration_score`| no       | Integer 1–5. Omitted → unchanged.                           |
| `delivery_score`     | no       | Integer 1–5. Omitted → unchanged.                            |
| `trust_score`        | no       | Integer 1–5. Omitted → unchanged.                            |
| `strengths_comment`  | no       | Free text. An empty string clears it; omitted → unchanged.   |
| `weaknesses_comment` | no       | Free text. An empty string clears it; omitted → unchanged.  |
| `visibility`         | no       | One of `"anonymous"`, `"named"`. Omitted → unchanged.        |

Expected response: `HTTP 200` with the updated draft JSON.

| Status | `message`                                                 | When                                              |
|--------|-----------------------------------------------------------|---------------------------------------------------|
| 400    | `"invalid request body"`                                  | Malformed/non-JSON body.                          |
| 400    | `"<validation message>"`                                  | A supplied score outside 1–5, or unknown `visibility`. |
| 401    | `"missing or invalid token"`                              | No/invalid `Authorization` header or bad token.   |
| 404    | `"feedback draft not found"`                              | No draft with this ID belongs to the caller.      |
| 409    | `"draft was modified concurrently; fetch it again and retry"` | The draft changed after this request read it. |
| 500    | `"failed"`                                                | Unexpected server error.                          |

---

## Submit Feedback Draft

`POST /v1/feedback-drafts/:id/submit` — submits the caller's draft,
transitioning it to the submitted state and releasing its
(reviewer, reviewee, period) slot so a new draft may be started immediately.
The request body is optional; when present it carries the same shape as the
update request and its fields are applied before validation (submit and
final-edit in one call).

Full validation runs here: all six scores must be present and in 1–5, both
comments must be non-empty, and the period's date window must be open.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X POST "http://localhost:1323/v1/feedback-drafts/<draft id>/submit" \
  -H "Authorization: Bearer <access_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "communication_score": 4,
    "leadership_score": 5,
    "technical_score": 3,
    "collaboration_score": 4,
    "delivery_score": 5,
    "trust_score": 2,
    "strengths_comment": "great teammate",
    "weaknesses_comment": "could document more",
    "visibility": "named"
  }'
```

Expected response: `HTTP 200`:

```json
{
  "id": "0190abcf-...",
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
  "visibility": "named",
  "status": "submitted",
  "created_at": "2026-08-18T10:00:00Z",
  "updated_at": "2026-08-18T11:00:00Z"
}
```

After submission the entry behaves exactly like one created via
`POST /v1/feedbacks`: it appears in the reviewee's and their manager's
listings (subject to the visibility policy) and can no longer be edited.

| Status | `message`                                             | When                                                  |
|--------|-------------------------------------------------------|-------------------------------------------------------|
| 400    | `"invalid request body"`                              | Malformed/non-JSON body.                              |
| 400    | `"<validation message>"`                              | A missing score, an empty comment, or an unknown `visibility`. |
| 401    | `"missing or invalid token"`                          | No/invalid `Authorization` header or bad token.       |
| 404    | `"feedback draft not found"`                          | No draft with this ID belongs to the caller (or it was already submitted). |
| 409    | `"draft was modified concurrently; fetch it again and retry"` | The draft changed after this request read it.   |
| 422    | `"feedback period is not open for submission"`        | The period has not started or has already ended.      |
| 500    | `"failed"`                                            | Unexpected server error.                              |

---

## Delete Feedback Draft

`DELETE /v1/feedback-drafts/:id` — removes the caller's draft and immediately
releases its (reviewer, reviewee, period) slot, so a new draft may be started
for the same pair right away. Deleting an entry that is missing or no longer a
draft returns `404`; submitted feedback is immutable.

```bash
curl -s -w "\nHTTP %{http_code}\n" -X DELETE "http://localhost:1323/v1/feedback-drafts/<draft id>" \
  -H "Authorization: Bearer <access_token>"
```

Expected response: `HTTP 204` with an empty body.

| Status | `message`                      | When                                            |
|--------|--------------------------------|-------------------------------------------------|
| 401    | `"missing or invalid token"`   | No/invalid `Authorization` header or bad token. |
| 404    | `"feedback draft not found"`   | No draft with this ID belongs to the caller.    |
| 500    | `"failed"`                     | Unexpected server error.                        |

---

## List Feedback Received by an Employee (Manager View)

`GET /v1/employees/:id/feedbacks` — returns one page of feedback entries
received by the named employee, but only when the authenticated caller is
that employee's manager. Ordered by `created_at` descending (newest first).
Requires a valid `Bearer` JWT.

Authorization is a fresh-load check: the reviewee's current `manager_id`
must equal the caller's ID. A caller who is not the reviewee's manager —
including the reviewee themselves — gets `403`; an unknown employee ID
gets `404`.

Visibility policy: the manager never sees who wrote an entry — `reviewer_id`
is always empty in this view, including for entries with
`visibility: "named"`. The comments themselves are returned in full. This
differs from the reviewee's own view (`GET /v1/me/feedbacks`), where named
entries still include `reviewer_id`.

Pagination is cursor-based and controlled by the same two optional query
parameters as `GET /v1/me/feedbacks`:

| Parameter | Default | Notes                                                                                  |
|-----------|---------|----------------------------------------------------------------------------------------|
| `limit`   | `20`    | Page size. Non-numeric or `<= 0` falls back to the default; values above `100` are capped. |
| `cursor`  | —       | The `next_cursor` value from the previous page (a feedback ID). Omit on the first page. An unknown cursor returns `400`. |

```bash
curl -s -w "\nHTTP %{http_code}\n" -X GET "http://localhost:1323/v1/employees/<reviewee id>/feedbacks?limit=20" \
  -H "Authorization: Bearer <manager access_token>"
```

Expected response: `HTTP 200`:

```json
{
  "feedbacks": [
    {
      "id": "0190abcd-...",
      "period_id": "0190abca-...",
      "reviewee_id": "<the reportee's id>",
      "reviewer_id": "",
      "communication_score": 4,
      "leadership_score": 5,
      "technical_score": 3,
      "collaboration_score": 4,
      "delivery_score": 5,
      "trust_score": 2,
      "strengths_comment": "great teammate",
      "weaknesses_comment": "could document more",
      "visibility": "named",
      "status": "submitted",
      "created_at": "2026-08-18T10:00:00Z",
      "updated_at": "2026-08-18T10:00:00Z"
    }
  ],
  "next_cursor": null
}
```

Note `reviewer_id` is empty even though `visibility` is `named` — the manager
view blinds reviewer identities unconditionally.

| Status | `message`                                            | When                                                        |
|--------|------------------------------------------------------|-------------------------------------------------------------|
| 400    | `"unknown cursor"`                                  | `cursor` does not refer to an existing feedback entry.       |
| 400    | `"<validation reason>"`                              | Malformed query values.                                     |
| 401    | `"missing or invalid token"`                         | No/invalid `Authorization` header or bad token.             |
| 403    | `"only the employee's manager can view their feedback"` | The caller is not the reviewee's current manager.        |
| 404    | `"employee not found"`                               | No employee matches the reviewee ID.                       |
| 500    | `"failed to list feedbacks"`                         | Unexpected server error.                                   |
