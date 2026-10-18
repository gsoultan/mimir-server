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

A model of any other shape can come as a Mimir Model file: choose the
**Mimir Model file** type and upload a `.mim` file, written by mimir's
`mim.Export` or converted from PyTorch, ONNX, GGUF or safetensors with the
`mim` tool. The file holds the whole model, so the server and its workers
run it as it is. It must take one fixed-shape float input; its first output
is the answer, and a `mimir.labels` list in its metadata names the classes.

## Who can do what

| Role | Can |
| --- | --- |
| Viewer | See models and ask them questions |
| Editor | Also add, rename and delete models |
| Admin | Also manage their team's people and webhooks, read its audit log and teach from a database |
| Owner | Also manage admins, owners, teams, workers and every server setting |

Viewers and editors can be limited to chosen models. Admins and owners
reach every model in their team. Each person makes their own API keys
under **Connect your apps**; a key can never do more than its owner.

### Teams

One server can hold several teams that don't see each other's work. Each
person belongs to one team and sees only its models, examples, training
runs, webhooks, audit events and usage; asking for another team's model
by id reads as "not found". A server starts with one team, "My team", and
everything made before teams existed belongs to it.

Owners manage teams under **Settings → Teams**: add, rename, remove an
empty one, and **Move me here** to work inside another team. When adding
or editing a person, owners pick their team. Moving someone signs them out
and revokes their API keys, since those were for the old team. Owners see
every team's people and audit events; workers, limits and the other
server-wide settings are theirs alone.

### Keeping accounts safe

- **Two-step verification.** Under **Security** in the account menu,
  anyone can add an authenticator app (Google Authenticator, Authy,
  1Password and the like) to their sign-in, and gets eight one-time
  recovery codes for a lost phone. Under **Settings → Security** an owner
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
models first says what their data looks like, then trains in three steps:

- **A table**: rows and columns from a spreadsheet or a database.
- **Text**: reviews, messages or notes, one per cell next to the answer.
- **Photos in folders**: one folder per answer, as a ZIP or a dropped folder.
- **Photos with details**: a ZIP with `data.csv` and the photos it names.

1. **The examples.** Upload a `.csv` with a header row and pick the column
   that holds the answer, upload photos, query a database, or use the
   built-in handwritten digits (MNIST, downloaded once into the data
   folder). Columns can hold numbers, categories (a word from a short
   list), yes/no, free text, or a photo's file name; the server tells
   them apart and turns each into numbers. CSVs go up to 64 MB, a million
   rows and a thousand columns; ZIPs up to 200 MB and 30,000 photos.
2. **What kind.** The model types that fit the examples, plus a name:
   a table or text classifier, a **number predictor** when the answer is
   a number, a **picture classifier** for photo folders, and a **photo
   and table classifier** when each row has a photo too.
3. **How long.** Quick look (3 passes), Normal (10) or Thorough (30), with
   batch size, learning rate and the share kept aside for scoring under
   *Expert settings*.

A number predictor can't be exactly right, so its score counts answers
within a tenth of the real one, and the run page shows how far off it
usually is.

### Templates

**Templates** lists 46 business problems across manufacturing, quality
control, sales, shops, finance, people, logistics and support: "which
customers won't renew", "is this weld OK", "how much energy will the
next shift use". Each says exactly what data to bring, with a
downloadable made-up example (a CSV, a ZIP of photo folders, or a ZIP
with `data.csv` and photos) and a starting SQL query. **Use this
template** opens the wizard with the template filled in; **Try it with
the example data** teaches from the example in a few clicks.

When data comes in, the server checks it against the template before
anything runs: which columns were found, the answer column and how its
answers are spread, empty or unreadable cells, too few rows, missing
photos. A column under another name can be matched to the template's
from a list. The same check is in the API as `CheckDataset`, and the
catalog as `TemplateService` (`proto/mimir/v1/template.proto`).

Runs queue up and train one at a time in the server process. The run page
shows the pass and step, a chart of right answers and loss per pass, and
the score on examples the model never saw while learning. A run can be
stopped and resumed: a checkpoint is written after every pass. When a run
finishes, the model appears under **Models** with its columns and answers,
so **Try a model** shows one input per column (a list for categories,
yes/no, a text box, a photo picker). Apps send the row by column name in
`fields` and a photo in `photo`, and the server turns them into numbers
the same way it did while teaching. Table models standardize their
columns inside the model, so apps send the raw values.

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

# A model taught from a table: the row by column name.
curl localhost:8080/mimir.v1.PredictService/Predict \
  -H 'Authorization: Bearer mimir_...' \
  -H 'Content-Type: application/json' \
  -d '{"modelId": "<id>", "fields": {"seats": "25", "plan": "team"}}'
```

A number predictor answers with `number: true` and the answer in `value`.

gRPC runs over unencrypted HTTP/2 here; put a TLS proxy in front for
anything public. Health checks are at `grpc.health.v1.Health`.

### Webhooks

Under **Connect your apps** you can add a webhook: an address of yours
that mimir calls when a model is created, updated or deleted, when a
person is added or removed, or when a training run is queued, finishes or
fails. A webhook hears only its own team's events. Each call is a JSON
POST signed with the webhook's secret:

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

## Console and workers

mimir-server comes in two parts, both in the same binary:

- **The console** (`-role=console`) is the web app and the API: people,
  teams, models, examples and the training queue. It runs no models itself.
- **Workers** (`-role=worker`) are the machines that run and teach the
  models for it: a bigger box, a GPU, or several.

The default, `-role=standalone`, is both in one process for a single
machine; it can take workers too. (`-role=server` still works as the old
name for `console`.)

Under **Settings → Workers**, **Add a worker** gives a token and the
command to run on that machine:

```sh
mimir-server -role=worker -server https://mimir.example.com -token mimirw_...
```

The worker connects out to the console (no ports to open on its side),
says what it can run, and shows as online. Weights travel to a worker once
per connection. A stopped worker's runs go back to the queue.

### Where work runs

- **Each model has a "Runs on" choice** on its page: *Automatic* sends
  questions to the least busy worker that can run it, or pick one worker,
  such as the one with the GPU. A model on an offline worker answers with
  a clear "offline" error rather than moving elsewhere.
- **The teach wizard has a "Train on" choice.** A run placed on an offline
  worker waits for it without holding up other runs, and the model it
  makes runs on the same worker.
- Removing a worker puts its models and waiting runs back on Automatic.

### Dashboards

**Settings → Workers** shows every worker as a card: a status light, CPU,
memory and storage bars, and tasks running, plus totals across them all.
A card turns amber when the machine is over 90% full, and a warning names
models or runs stuck on an offline worker.

Click a worker for its own page: live gauges, charts of CPU, memory,
storage and tasks over the last hour, day or week (one reading a minute,
kept for a week), what it is running now, what is placed on it, and how
it is connected.

### Keeping the link secure

- **Encrypted.** A worker refuses a plain `http://` console on another
  machine, and the console refuses unencrypted workers from other
  machines. Serve the console with TLS (`-tls-cert` and `-tls-key`, or
  `-tls-auto` to make a certificate in the data folder) or put it behind an
  https proxy. For a private network you trust, `-insecure` on the worker
  and `-allow-plain-workers` on the console turn this off.
- **The worker checks the console.** When the console serves a
  certificate nobody vouches for (such as `-tls-auto`), tokens carry its
  key's fingerprint and the worker accepts only that key. Otherwise it
  checks the certificate the usual way.
- **The console checks the worker.** On its first connection a worker
  makes an Ed25519 key (kept in its data folder) and the console records
  it. Every connection after that must sign a fresh challenge with it, so
  a copied token is useless on another machine.
- **Tokens** are stored only as hashes and shown once. **New token** on a
  worker's page cuts the old one off and forgets the key, for a leaked
  token or a rebuilt machine. Joins, refusals, new addresses and new
  tokens go in the audit log and on the worker's page.

For models a weights file can't describe, write a worker of your own with
the `worker` package: implement `Predict` (and `Train`) for an
architecture name and call `worker.Run`. Workers authenticate with their
token and key only; they never see people's credentials.

## Settings

Owners find these under **Settings**; admins see their team's
**Audit log** and **Usage** there:

- **Alerts**: an email (SMTP) and/or Telegram message when a webhook keeps
  failing. Each has a test button.
- **Log retention**: how long audit events and webhook deliveries are kept.
  A cleanup runs every hour.
- **Database**: engine, file, size and counts, plus **Download a backup**
  (`GET /api/backup`, owners only), a consistent copy of the SQLite file.
  Model weights live next to it in the data folder.
- **Limits**: how many predictions each API key or person may make a minute,
  how many each person may make a month (all their keys together), and how
  many training runs each person may have waiting or learning at once. 0
  means no limit. Over a limit, calls fail with `resource_exhausted` (HTTP
  429) and a `Retry-After` header, and the message says when to try again.
- **Usage**: predictions per person and per key, month by month. Everyone
  sees their own use under **Connect your apps**.

## Develop

You need Go and [Bun](https://bun.sh). Then:

```sh
make dev
```

Open http://localhost:5173. The web app reloads as you edit it, and the
server rebuilds and restarts whenever a Go or SQL file changes (a build
error is printed and the old server keeps running). Data goes to
`data/dev`, so it survives restarts; delete that folder to start over.
Webhooks may point at local addresses. Stop everything with Ctrl+C.

| Setting | Default | What it does |
| --- | --- | --- |
| `MIMIR_DEV_DATA` | `./data/dev` | Data folder |
| `MIMIR_DEV_ADDR` | `localhost:8080` | Where the Go server listens |
| `MIMIR_DEV_ARGS` | | Extra server flags, such as `-role console` |

```sh
make test       # go vet, go test, web typecheck and unit tests
make gen        # regenerate code after editing proto/ (needs protoc-gen-go
                # and protoc-gen-connect-go on PATH)
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
