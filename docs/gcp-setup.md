# GCP Setup for CI/CD (one-time)

This guide sets up everything the `.github/workflows/ci.yml` pipeline needs:
unit tests → build & push to Artifact Registry → deploy to Cloud Run.

All commands use these placeholders — replace or export them first:

```bash
export PROJECT_ID="your-gcp-project-id"        # e.g. echo-prod
export REGION="asia-southeast1"
export AR_REPO="echo"
export RUN_SERVICE="echo-api"
export GH_REPO="tsongpon/echo"                 # owner/repo on GitHub
export DEPLOYER_SA="echo-github-deployer"
export RUNTIME_SA="echo-api-runtime"
```

## 1. Enable required APIs

```bash
gcloud services enable \
  run.googleapis.com \
  artifactregistry.googleapis.com \
  iamcredentials.googleapis.com \
  firestore.googleapis.com
```

## 2. Create Artifact Registry repository

```bash
gcloud artifacts repositories create "$AR_REPO" \
  --repository-format=docker \
  --location="$REGION" \
  --description="Container images for the echo API"
```

## 3. Create service accounts

**Deployer SA** — used by GitHub Actions to push images and deploy:

```bash
gcloud iam service-accounts create "$DEPLOYER_SA" \
  --display-name="GitHub Actions deployer"

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:${DEPLOYER_SA}@${PROJECT_ID}.iam.gserviceaccount.com" \
  --role="roles/run.admin"

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:${DEPLOYER_SA}@${PROJECT_ID}.iam.gserviceaccount.com" \
  --role="roles/artifactregistry.writer"

# Lets the deployer attach the runtime SA to the Cloud Run service.
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:${DEPLOYER_SA}@${PROJECT_ID}.iam.gserviceaccount.com" \
  --role="roles/iam.serviceAccountUser"
```

> `roles/run.admin` already lets the deployer grant `roles/run.invoker`
> (including to `allUsers`), so no extra IAM is needed for public access.

**Runtime SA** — the identity the running container uses for Firestore:

```bash
gcloud iam service-accounts create "$RUNTIME_SA" \
  --display-name="echo-api runtime (Cloud Run)"

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:${RUNTIME_SA}@${PROJECT_ID}.iam.gserviceaccount.com" \
  --role="roles/datastore.user"
```

## 4. Workload Identity Federation (keyless GitHub auth)

```bash
# Pool
gcloud iam workload-identity-pools create github-pool \
  --location=global \
  --display-name="GitHub Actions pool"

# Provider — trusts OIDC tokens from *this* GitHub repo only
gcloud iam workload-identity-pools providers create-oidc github-provider \
  --location=global \
  --workload-identity-pool=github-pool \
  --display-name="GitHub provider" \
  --issuer-uri="https://token.actions.githubusercontent.com" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository" \
  --attribute-condition="assertion.repository == '${GH_REPO}'"
```

**Allow the deployer SA to be impersonated via this provider:**

```bash
gcloud iam service-accounts add-iam-policy-binding \
  "${DEPLOYER_SA}@${PROJECT_ID}.iam.gserviceaccount.com" \
  --role="roles/iam.workloadIdentityUser" \
  --member="principalSet://iam.googleapis.com/projects/PROJECT_NUMBER/locations/global/workloadIdentityPools/github-pool/attribute.repository/${GH_REPO}"
```

> Replace `PROJECT_NUMBER` with your numeric project number:
> `gcloud projects describe "$PROJECT_ID" --format="value(projectNumber)"`

## 5. First deploy + runtime env vars (set once)

Deploy once with `gcloud` to create the service, attach the runtime SA, and
set environment variables. The workflow later only updates the image.

```bash
IMAGE="${REGION}-docker.pkg.dev/${PROJECT_ID}/${AR_REPO}/api:main"

# Note: --image uses {REGION}-docker.pkg.dev/{PROJECT}/{REPO}/{IMAGE} — three
# path components after the host. "${AR_REPO}:main" alone would be invalid.

gcloud run deploy "$RUN_SERVICE" \
  --region="$REGION" \
  --image="$IMAGE" \
  --service-account="${RUNTIME_SA}@${PROJECT_ID}.iam.gserviceaccount.com" \
  --set-env-vars="JWT_SECRET=<generate-a-long-random-secret>,FIRESTORE_PROJECT_ID=${PROJECT_ID},FIRESTORE_DATABASE_NAME=(default),APP_BASE_URL=<your-cloud-run-url>,RESEND_API_KEY=<resend-key>,RESEND_FROM_EMAIL=<verified-sender>" \
  --allow-unauthenticated \
  --min-instances=0 \
  --max-instances=2 \
  --cpu=1 \
  --memory=256Mi
```

Generate a JWT secret: `openssl rand -hex 32`

To update env vars later (e.g. rotating the JWT secret):

```bash
gcloud run services update "$RUN_SERVICE" \
  --region="$REGION" \
  --set-env-vars="JWT_SECRET=<new-secret>"
```

> `GOOGLE_APPLICATION_CREDENTIALS` stays empty — on Cloud Run the runtime SA
> is picked up from the platform automatically.

## 5b. Public internet access

The commands above use `--allow-unauthenticated`, which creates the
`allUsers:roles/run.invoker` IAM binding that makes the service reachable from
the public internet without a Google-signed token. The CI/CD workflow passes
`--allow-unauthenticated` on every deploy too, so public access persists
across deployments.

If the service already exists and was deployed private, make it public:

```bash
gcloud run services add-iam-policy-binding "$RUN_SERVICE" \
  --region="$REGION" \
  --member="allUsers" \
  --role="roles/run.invoker"
```

Verify unauthenticated access works from anywhere:

```bash
URL=$(gcloud run services describe "$RUN_SERVICE" --region="$REGION" --format='value(status.url)')
curl -s "${URL}/ping"   # → pong
```

> Note: public here means "anyone can *reach* the API". Auth-sensitive
> endpoints still require a valid JWT (`Authorization: Bearer ...`) — only
> `/ping`, the OpenAPI spec, and register/login/verify are anonymous.

## 6. GitHub repo secrets

Add these under **Settings → Secrets and actions → Actions → New repository secret**:

| Secret | Value |
|---|---|
| `GCP_PROJECT_ID` | `$PROJECT_ID` |
| `GCP_WIF_PROVIDER` | `projects/PROJECT_NUMBER/locations/global/workloadIdentityPools/github-pool/providers/github-provider` |
| `GCP_DEPLOYER_SA_EMAIL` | `${DEPLOYER_SA}@${PROJECT_ID}.iam.gserviceaccount.com` — the SA GitHub Actions impersonates (needs `roles/run.admin`, `roles/artifactregistry.writer`, `roles/iam.serviceAccountUser`) |
| `CLOUD_RUN_RUNTIME_SA_EMAIL` | `${RUNTIME_SA}@${PROJECT_ID}.iam.gserviceaccount.com` |

## 7. GitHub environment (optional, recommended)

Under **Settings → Environments → New environment → `production`** you can add
required reviewers so the deploy job pauses for approval before production.

## Verify

Push to `main`. In the **Actions** tab you should see three green jobs:
`Unit tests` → `Build & push image` → `Deploy to production (Cloud Run)`.

Check the live service:

```bash
curl "https://$(gcloud run services describe echo-api --region=asia-southeast1 --format='value(status.url)')/ping"
# → pong
```
