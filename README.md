# mimir-server

A server for [mimir](https://github.com/gsoultan/mimir) models. Upload a
model's weights, then ask it questions from the web UI, over HTTP/JSON, or
over gRPC, all on one port.

One binary, a SQLite database, a model store, prediction, training from
your own examples, accounts with roles and per-model access, API keys, an
audit log, webhooks, alerts, log retention, database backup and the web
UI, and workers on other machines that run and teach models for it.

## Run it

You need Go 1.27 and [Bun](https://bun.sh).

```sh
make            # builds the web UI, then bin/mimir-server with it embedded
./bin/mimir-server -addr :8080 -data ./data
```

Open http://localhost:8080. The first visit asks you to make the owner
account. Then choose **Add a model**, pick the model type and upload a
`.safetensors` file saved with `nn.Save` from mimir.

## Who can do what

| Role | Can |
| --- | --- |
| Viewer | See models and ask them questions |
| Editor | Also add, rename and delete models |
| Admin | Also manage people and read the audit log |
| Owner | Also manage admins and owners |

Viewers and editors can be limited to chosen models. Admins and owners
reach every model. Each person makes their own API keys under **Connect
your apps**; a key can never do more than its owner.

### Keeping accounts safe

- **Two-step verification.** Under **Security** in the account menu,
  anyone can add an authenticator app (Google Authenticator, Authy,
  1Password and the like) to their sign-in, and gets eight one-time
  recovery codes for a lost phone. Under **Settings → Security** an admin
  can require it for admins and owners, or for everyone; people without it
  can only set it up until it's on. An admin can reset it for someone who
  lost their phone.
- **Sessions.** Each browser gets its own session, listed under
  **Security** with a sign-out button and a "sign out everywhere else"
  button. A session ends after 30 days, or 7 days without use, and is tied
  to the browser that started it: a cookie replayed from a different
  browser ends the session instead of opening it. A new token is minted at
  every sign-in, after the second factor and after a password change;
  a password change, a smaller role or a disabled account signs the person
  out everywhere.
- **Guessing and forgery.** Ten wrong passwords or codes lock an account
  (and the address they came from) for 15 minutes. Cross-site requests
  that carry the session cookie are refused, and the UI ships with a
  Content-Security-Policy, `X-Frame-Options: DENY` and friends.
- **Storage.** Passwords are hashed with argon2id; session tokens, API
  keys and recovery codes are stored only as hashes. The cookie is
  HttpOnly and SameSite=Lax, and becomes `__Host-` prefixed and Secure
  behind an HTTPS proxy that sets `X-Forwarded-Proto`.

## How models load

A mimir model is Go code plus weights, so the server keeps a registry of
known model types in `internal/arch` (today: two handwritten-digit models,
`mnist-mlp` and `mnist-cnn`, and `table-mlp` for rows of numbers). An
upload names its type, and the server checks the weights fit that type
before storing them. To serve your own architecture, add it to the
registry.

## Teach a new model

Under **Models → Teach a new model** (or **Training**), anyone who can add
models can train one in three steps:

1. **The examples.** Upload a `.csv` with a header row and pick the column
   that holds the answer; every other column must be a number. Or use the
   built-in handwritten digits (MNIST, downloaded once into the data
   folder). Up to 64 MB, a million rows, a thousand columns and a thousand
   different answers.
2. **What kind.** The model types that fit the examples, plus a name.
3. **How long.** Quick look (3 passes), Normal (10) or Thorough (30), with
   batch size, learning rate and the share kept aside for scoring under
   *Expert settings*.

Runs queue up and train one at a time in the server process. The run page
shows the pass and step, a chart of right answers and loss per pass, and
the score on examples the model never saw while learning. A run can be
stopped and resumed: a checkpoint is written after every pass. When a run
finishes, the model appears under **Models** with its columns and answers,
so **Try a model** shows one box per column and `Predict` takes the
numbers in that order. Table models standardize their columns inside the
model, so apps send the raw numbers.

### From a database, and keeping it learning

Admins can also pick **From a database** in the first step: choose
PostgreSQL, MySQL or SQLite, give the connection and a `SELECT`, and **Try
the query** to see the columns and pick the answer. The query runs in a
read-only transaction (SQLite files open read-only), returns up to a
million rows, and can't point at the server's own data folder. The
connection string stays on the server and is never sent back.

Examples from a query can **keep learning** every hour, day or week. When
a pass is due the server runs the query again, then teaches the same
model further from where it left off. The model keeps its id, so apps
keep working, and it is only replaced when the new pass scores at least as
well on held-out rows; otherwise the run says it kept the previous
version. If the columns or answers changed, the pass learns from scratch.
The run page shows the schedule, the next pass and what each pass did,
and the schedule can be changed or turned off there.

Datasets and runs are also in the API (`DatasetService`, `TrainingService`
in `proto/mimir/v1/training.proto`), and webhooks can listen for
`training.queued`, `training.finished` and `training.failed`.

## Use it from your app

Every method works over Connect (HTTP/JSON), gRPC and gRPC-Web. The API is
in `proto/mimir/v1/model.proto`.

```sh
curl localhost:8080/mimir.v1.PredictService/Predict \
  -H 'Authorization: Bearer mimir_...' \
  -H 'Content-Type: application/json' \
  -d '{"modelId": "<id>", "input": {"values": [/* 784 numbers from 0 to 1 */]}}'

grpcurl -plaintext -H 'Authorization: Bearer mimir_...' \
  -d '{"model_id": "<id>", "input": {"values": [...]}}' \
  localhost:8080 mimir.v1.PredictService/Predict
```

gRPC runs over unencrypted HTTP/2 here; put a TLS proxy in front for
anything public. Health checks are at `grpc.health.v1.Health`.

### Webhooks

Under **Connect your apps** you can add a webhook: an address of yours
that mimir calls when a model is created, updated or deleted, when a
person is added or removed, or when a training run is queued, finishes or
fails. Each call is a JSON POST signed with the
webhook's secret:

```
Mimir-Event: model.created
Mimir-Event-Id: <id>
Mimir-Delivery-Id: <id>
Mimir-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256 of "<t>.<body>">
```

Failed calls are retried with backoff for about a day, and every attempt
shows in the webhook's history with a **Redeliver** button. Addresses on
private networks are refused unless the server runs with
`-allow-private-webhooks`.

## Workers

One server can hand predictions and training to workers on other
machines: a bigger box, a GPU, or several. Under **Settings → Workers**,
**Add a worker** gives a token and the command to run there:

```sh
mimir-server -role=worker -server https://mimir.example.com -token mimirw_...
```

The worker connects out to the server (no ports to open on its side),
says what it can run, and shows as online. Training runs go to a free
worker that trains the model's type; predictions go to workers when the
server runs with `-role=server`, which makes it coordinate only, and stay
local in the default `-role=standalone`. Weights travel to a worker once
per connection. A stopped worker's runs go back to the queue.

For models a weights file can't describe, write a worker of your own with
the `worker` package: implement `Predict` (and `Train`) for an
architecture name and call `worker.Run`. Workers authenticate with their
token only; they never see people's credentials.

## Settings

Admins find these under **Settings**:

- **Alerts**: an email (SMTP) and/or Telegram message when a webhook keeps
  failing. Each has a test button.
- **Log retention**: how long audit events and webhook deliveries are kept.
  A cleanup runs every hour.
- **Database**: engine, file, size and counts, plus **Download a backup**
  (`GET /api/backup`, admin only), a consistent copy of the SQLite file.
  Model weights live next to it in the data folder.
- **Limits**: how many predictions each API key or person may make a minute,
  how many each person may make a month (all their keys together), and how
  many training runs each person may have waiting or learning at once. 0
  means no limit. Over a limit, calls fail with `resource_exhausted` (HTTP
  429) and a `Retry-After` header, and the message says when to try again.
- **Usage**: predictions per person and per key, month by month. Everyone
  sees their own use under **Connect your apps**.

## Develop

```sh
make test       # go vet, go test, web typecheck and unit tests
make gen        # regenerate code after editing proto/ (needs protoc-gen-go
                # and protoc-gen-connect-go on PATH)
cd web && bun run dev   # UI on :5173, API calls proxied to :8080
```

| Path | What's there |
| --- | --- |
| `cmd/mimir-server` | The binary |
| `internal/api` | Connect handlers |
| `internal/arch` | Known model types |
| `internal/alert` | Email and Telegram alerts |
| `internal/auth` | Passwords, sessions, API keys, permissions |
| `internal/runtime` | Loads weights into mimir and runs them |
| `internal/store` | SQLite metadata and weight files |
| `internal/webhook` | Signing, delivery and retries |
| `internal/server` | HTTP wiring, health, web UI serving |
| `web` | React 19, Mantine 9, TanStack Router and Query, Connect, PWA |
