# mimir-server

A server for [mimir](https://github.com/gsoultan/mimir) models. Upload a
model's weights, then ask it questions from the web UI, over HTTP/JSON, or
over gRPC, all on one port.

One binary, a PostgreSQL database, a model store, prediction, training from
your own examples, accounts with roles and per-model access, API keys, an
audit log, webhooks, alerts, log retention, database backup and the web
UI, and workers on other machines that run and teach models for it.

## Run it

You need Go 1.27, [Bun](https://bun.sh) and PostgreSQL (14 or later).

```sh
make            # builds the web UI, then bin/mimir-server with it embedded
createdb mimir
./bin/mimir-server -addr :8080 -data ./data \
  -database-url 'postgres://mimir:secret@localhost:5432/mimir?sslmode=disable'
```

The server keeps its accounts, models, runs and settings in PostgreSQL;
`-database-url` (or `MIMIR_DATABASE_URL`) is required for the standalone
and console roles. On start it brings the schema up to date by itself
(only the first start or an upgrade changes anything), so the role needs
`CREATE` on the database. Pool options such as `pool_max_conns=32` may go
in the URL; the default is at most 16 connections. Model weights and
uploaded examples stay as files in the `-data` folder.

Secrets the server keeps in the database (webhook signing secrets, dataset
connection strings, alert passwords and bot tokens) are encrypted with
AES-256-GCM, so a copy or backup of the database does not reveal them. The
key is 32 random bytes as base64 or hex (`openssl rand -base64 32`), taken
from `MIMIR_SECRET_KEY`, else from the file `-secret-key-file` (or
`MIMIR_SECRET_KEY_FILE`) names, else from `<data>/secret.key`, which the
server makes on first start and logs. Secrets written by an older version
are encrypted in place on the next start. Keep the key with your backups
but apart from them: without it those secrets cannot be read, and the
server refuses to start with a key that does not open them. Every console
sharing a database needs the same key.

Open http://localhost:8080. The first visit asks you to make the owner
account, with the one-time setup token the server prints in its log
(`"setup_token"`) when it starts with no accounts; set
`MIMIR_SETUP_TOKEN` (at least 16 characters) to choose it yourself, for
an automated install. Until the owner exists, nobody without the token can
claim the server. Then choose **Add a model**, pick the model type and upload a
`.mim` file of its weights, saved with `mim.Save` from mimir. Models the
server trains are stored the same way; the Mimir Model file is the only
format it reads or writes.

A model of any other shape can come as a whole Mimir Model file: choose the
**Mimir Model file** type and upload a `.mim` file written by mimir's
`mim.Export`, or converted once from PyTorch or safetensors with
`mim-import`. The file holds the whole model, so the server and its workers
run it as it is. It must take one float input of a fixed shape (a free
batch size is fine; questions come one at a time); its first output is the
answer, and a `mimir.labels` list in its metadata names the classes.

### ONNX and GGUF models

You can also upload an ONNX or a GGUF file as it is. The server converts it
to a Mimir Model file when you add it, stores only that, and runs it like
any other, so nothing downstream sees the original format.

- **ONNX**, under **Mimir Model file**: convolutional and fully connected
  networks in float32, opsets 7 to 22, built from Conv,
  BatchNormalization, Relu, Clip, Sigmoid, Tanh, MaxPool, AveragePool,
  GlobalAveragePool, Add, Sub, Mul, Div, Gemm, MatMul, Flatten, Reshape,
  Transpose, Concat, Softmax, Identity, Dropout and Constant. The graph
  and its weights carry over as they are.
- **GGUF**, under **Language model**: decoder-only language models of the
  `llama` architecture (Llama 1 to 3, TinyLlama, Mistral without sliding
  windows, SmolLM) or `qwen2` (Qwen 2 and 2.5), with F32, F16, BF16, Q8_0,
  Q4_0, Q4_K or Q6_K weights, so the common Q4_K_M and Q8_0 downloads work;
  Q5_K_M, Q3_K, Q2_K and the IQ types don't yet. The weights are kept as
  they are, and the server adds the forward pass GGUF doesn't carry, with a
  key-value cache of the model's context length, up to 4096 tokens.
  **Try a model** then chats with it, or gives it the start of a text to
  continue, with the answer appearing word by word. Apps call
  `PredictService.Chat`, `Generate` or `GenerateStream`, or the
  OpenAI-compatible API under `/v1` (see Use it from your app). Language
  models run on the console itself, one generation at a time per model,
  not on workers.

  A chat is written out in the format the model was trained on: ChatML
  (Qwen, SmolLM), Llama 3, Zephyr (TinyLlama Chat), `[INST]` (Llama 2,
  Mistral) or Gemma, picked from the markers in the model's chat template.
  The template itself is never run (running a GGUF Jinja template was
  CVE-2024-34359); a model with none gets a plain "User: / Assistant:"
  format. Each language model keeps default instructions (the system
  message), word choice and longest answer, set on its page or from the
  chat, which apps get when they don't send their own. Chats in **Try a
  model** are kept for 30 days in the browser, not on the server.

The server goes by what the file holds: a GGUF file added as a Mimir Model
file still becomes a language model.

Anything else, such as an ONNX model with an operator outside that list,
a recurrent or quantized ONNX model, a GGUF file of another architecture
or with RoPE scaling, or a safetensors file, is refused when you add it,
with a message that names what isn't supported.

Uploads are limited to 256 MB by default, which fits ONNX models and small
language models. Start the console with `-max-model-mb 2048`, say, to take
bigger GGUF files; converting one needs about three times its size in
memory for a moment.

### In a container, or from a release

The `Dockerfile` builds the web UI and a static binary (no GPU backends:
it is built with `-tags nocuda,norocm`, since mimir loads the CUDA and ROCm
libraries through the C library's loader) on a distroless image that runs
as a non-root user, with the data folder at `/data`:

```sh
docker build --build-arg VERSION=$(git describe --tags --always) -t mimir-server .
docker run -p 8080:8080 -v mimir-data:/data \
  -e MIMIR_DATABASE_URL='postgres://mimir:secret@db:5432/mimir' mimir-server
```

The image has no `pg_dump`, so download backups with `pg_dump` from
elsewhere.

Pushing a `v*` tag runs [GoReleaser](https://goreleaser.com)
(`.goreleaser.yaml`): it builds release archives for Linux, macOS and
Windows on amd64 and arm64, with SHA-256 sums, and attaches them to a
GitHub release with notes from the merged pull requests.
The release binaries keep the GPU backends, so on Linux they need glibc.
`goreleaser release --snapshot --clean` tries a release locally into
`dist/`. `make build` and CI stamp the version (`git describe`) with
`-ldflags "-X main.version=..."`; workers report it to the console.

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
- **Guessing and forgery.** At sign-in, after five wrong passwords for
  an account from one address, each further one doubles the wait before
  that address may try that account again (1 second, 2, 4, ... up to 15
  minutes); and ten wrong passwords from one address, for any accounts,
  hold that address back for 15 minutes. Neither touches the account from
  anywhere else, so nobody can lock its owner out by failing on purpose.
  Ten wrong second-step codes, or wrong passwords when changing account
  security, hold that account back for 15 minutes. Cross-site requests
  that carry the session cookie are refused, and the UI ships with a
  Content-Security-Policy, `X-Frame-Options: DENY` and friends.
- **Behind a proxy.** The console believes `X-Forwarded-For` and
  `X-Forwarded-Proto` only from the proxies you list with
  `-trusted-proxies` (or `$MIMIR_TRUSTED_PROXIES`), a comma-separated list
  of addresses and CIDR ranges such as `127.0.0.1,10.0.0.0/8`. By default
  it trusts none and uses the address of the connection itself, so a
  client can't pick the address that keys rate limits and goes in the
  audit log, or claim it used HTTPS. Behind a reverse proxy, list it:
  otherwise every request seems to come from the proxy.
- **Storage.** Passwords are hashed with argon2id; session tokens, API
  keys and recovery codes are stored only as hashes. The cookie is
  HttpOnly and SameSite=Lax, and becomes `__Host-` prefixed and Secure,
  with HSTS on every response, over HTTPS: when the console serves TLS
  itself, or behind a trusted proxy that sets `X-Forwarded-Proto: https`.

## How models load

A mimir model is Go code plus weights, so the server keeps a registry of
known model types in `internal/arch` (today: two handwritten-digit models,
`mnist-mlp` and `mnist-cnn`, and `table-mlp` for rows of numbers). An
upload names its type, and the server checks the weights fit that type
before storing them. To serve a model type of your own without changing
the server, write it in Go and run it on a worker (see Your own model
types).

**Models → Build a model** is where a new model starts. It asks what the
model should do (yes or no, pick a group, a number, write or chat,
recommend, forecast, spot the odd one, find it in a photo), then ranks the
ways to get there: a template, your own data, a model file trained
elsewhere, or your own code.

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
   and table classifier** when each row has a photo too. A table model
   can be Small, Medium (the usual) or Large, or **designed** step by
   step: up to 8 "learn patterns" (dense, 1 to 1024 units) and "forget a
   little" (dropout) steps, with how many numbers it learns and how many
   examples that wants shown as you go. The design is saved in the model
   file, so any server or worker rebuilds it the same way.
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

A language model takes a prompt instead and answers with what it wrote:

```sh
curl localhost:8080/mimir.v1.PredictService/Generate \
  -H 'Authorization: Bearer mimir_...' \
  -H 'Content-Type: application/json' \
  -d '{"modelId": "<id>", "prompt": "Once upon a time", "maxTokens": 128, "temperature": 0.7}'
```

`maxTokens` is up to 1024 (128 by default) and `temperature` from 0, which
always picks the likeliest word, to 2. `GenerateStream` takes the same
request and sends the text piece by piece as it is written, and `Chat`
answers a list of `{role, content}` messages the same way, using the
model's default instructions, temperature and answer length unless the
request sets them.

### On people's own devices

A model can also answer in the browser, with no server and no network.
Turn on **Run on people's devices** on its page; then anyone who may use
it can pick **On this device** in Try a model. The first question saves
the model in their browser, and after that it answers there, offline too.
Their questions never reach the server, so they don't count in usage.
**On this device** in the menu lists what a browser has saved, offers the
new version after a model learns again, and opens with no network.

Table, photo and digits models up to 50 MB can run on devices. Language
models and models in your own code stay on the server. The browser runs
the same Go code as the server, built for WebAssembly (`cmd/mimir-wasm`),
so a model answers the same in both places. Apps can download a model
someone allowed onto devices with `ModelService/GetDeviceModel`.

### OpenAI-compatible API

Apps and libraries made for OpenAI work with the language models: point
their base URL at `/v1` and use a mimir API key. A model's name there is
its name in lower case with dashes (Tiny Llama is `tiny-llama`); its id
works too. Keys, per-model access and limits apply as for every other call.

```sh
curl localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer mimir_...' \
  -H 'Content-Type: application/json' \
  -d '{"model": "tiny-llama", "stream": true, "messages": [{"role": "user", "content": "Hello!"}]}'
```

```python
from openai import OpenAI
client = OpenAI(base_url="http://localhost:8080/v1", api_key="mimir_...")
print(client.chat.completions.create(model="tiny-llama", messages=[{"role": "user", "content": "Hello!"}]).choices[0].message.content)
```

It serves `GET /v1/models`, `POST /v1/chat/completions` and
`POST /v1/completions`, with `stream` (server-sent events, ending in
`data: [DONE]`, and `stream_options.include_usage`), `max_tokens` or
`max_completion_tokens`, `temperature`, `seed` and `stop` (up to four
strings). System and developer messages become the instructions. Tools,
images, `n` above 1 and log probabilities aren't supported and are refused
with a 400.

gRPC runs over unencrypted HTTP/2 here; put a TLS proxy in front for
anything public.

### Health, metrics and request ids

- `GET /healthz` answers 200 while the process serves HTTP (a liveness
  probe; it checks nothing else).
- `GET /readyz` answers 200 when the database answers a ping within two
  seconds, 503 otherwise (a readiness probe). `grpc.health.v1.Health`
  reports the same, for the whole server and for each service.
- `GET /metrics` serves Prometheus metrics: `mimir_http_requests_total`,
  `mimir_http_errors_total` (5xx) and `mimir_http_request_duration_seconds`
  by method and route; `mimir_rpc_requests_total`, `mimir_rpc_errors_total`
  and `mimir_rpc_duration_seconds` by Connect/gRPC procedure and status
  code; the models, `mimir_models_loaded`, `mimir_model_cache_bytes`,
  `mimir_model_loads_total` and `mimir_model_load_seconds` by kind; the
  predictions, `mimir_predictions_total`, `mimir_prediction_batch_size`,
  `mimir_prediction_batch_seconds` and `mimir_prediction_queue_seconds`
  (how long a prediction waited for its batch) by engine; plus Go runtime
  and process metrics. Set `-metrics-token` (or `MIMIR_METRICS_TOKEN`) to
  require `Authorization: Bearer <token>`; without it anyone who can reach
  the port can read them.

### Batching predictions

Predictions to one model queue up and run together as one forward pass,
a row per request, when they arrive faster than the model answers them:
while the model is idle a request runs at once and alone, so a lone
request pays no latency for it, and a small model answers a batch of 32
in little more time than one request. `-predict-batch` (or
`MIMIR_PREDICT_BATCH`, default 32) is the most requests in one batch and
1 turns batching off; `-predict-batch-wait` (`MIMIR_PREDICT_BATCH_WAIT`,
default 0) makes a runner that found fewer than that waiting hold them
for up to that long for more, trading latency for bigger batches, for a
server whose throughput matters more than each request's time. The
batch sizes and waits show in the metrics above.
- Every response carries `X-Request-Id`: the caller's own when it sends a
  sensible one (up to 128 letters, digits and `-_.:+/=`), otherwise a new
  UUID. Log lines written while handling the request carry it as
  `request_id`.

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
  https proxy listed in `-trusted-proxies`. For a private network you trust, `-insecure` on the worker
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

Workers authenticate with their token and key only; they never see
people's credentials.

### Your own model types

For models mimir can't build itself, such as a recommender or a
forecaster, write the model in Go with the `worker` package and run it as
a worker. **Build a model → Write your own model** walks through it:

1. **Describe it**: the name your code runs it under, what people send
   (a row of columns, a photo or a list of numbers) and what it answers
   (one of a few groups, a number, or a ranked list of a set number of
   items). This is `ModelService.SaveArchitecture`; the type then shows
   up wherever model types do.
2. **Write it**: the page gives a starting `main.go` that implements
   `Predict`, and `Train` to teach it here, and calls `worker.Run` with
   a handler under that name.
3. **Connect it**: make a worker token and run it. The page waits and
   moves on when a worker running that type joins.
4. **Teach it or upload what you trained**: teaching sends the examples
   to that worker's `Train`; an upload keeps the file as it is, with how
   many numbers the model reads and the answers it picks from.

Its models answer only on workers that run its code, even on a console
that runs models itself, and a ranked answer lists the type's number of
items, best first. A type can be changed by whoever described it or an
owner, and deleted once no model uses it.

## Settings

Owners find these under **Settings**; admins see their team's
**Audit log** and **Usage** there:

- **Alerts**: an email (SMTP) and/or Telegram message when a webhook keeps
  failing. Each has a test button.
- **Log retention**: how long audit events and webhook deliveries are kept.
  A cleanup runs every hour.
- **Database**: engine, server (password hidden), size and counts, plus
  **Download a backup** (`GET /api/backup`, owners only): a consistent
  `pg_dump --format=custom` copy, restored with `pg_restore`. The server
  needs `pg_dump` on its `PATH` for this (same major version as the
  database or newer); without it the download fails with HTTP 501 and you
  run `pg_dump` yourself. Model weights live in the data folder; back that
  up too. Stored secrets are encrypted in the dump; restoring needs the
  same secret key.
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

Tests that need a database read `MIMIR_TEST_DATABASE_URL`, a PostgreSQL
URL whose role may create databases; each test makes its own database and
drops it after. Without it those tests are skipped.

```sh
make test       # go vet, go test, web typecheck and unit tests
make gen        # regenerate code after editing proto/ (needs protoc-gen-go
                # and protoc-gen-connect-go on PATH)
make wasm       # rebuild the browser runtime into web/dist/wasm
```

`make web` builds the browser runtime too. The web app bundles Go's
`wasm_exec.js` from `web/src/workers`; after moving to a new Go version,
`make wasm` says to copy it again.

| Path | What's there |
| --- | --- |
| `cmd/mimir-server` | The binary |
| `internal/api` | Connect handlers |
| `internal/arch` | Known model types |
| `internal/alert` | Email and Telegram alerts |
| `internal/auth` | Passwords, sessions, API keys, permissions |
| `internal/runtime` | Loads weights into mimir and runs them |
| `internal/ask` | Turns a question by column name into the numbers a model reads |
| `internal/device`, `cmd/mimir-wasm` | Models answering in the browser |
| `internal/store` | PostgreSQL metadata (through [storm](https://github.com/gsoultan/storm)) and weight files |
| `internal/webhook` | Signing, delivery and retries |
| `internal/server` | HTTP wiring, health, web UI serving |
| `web` | React 19, Mantine 9, TanStack Router and Query, Connect, PWA |

### The database schema

The tables are Go structs in `internal/store/tables`, one per file, with
the shared columns as mixins (`mixins.go`: id and creation time, display
name, owning team) and the roles, statuses and kinds as named types
(`enums.go`) that package store uses directly. The typed query code
in `internal/store/db` and the migrations in `internal/store/migrations`
are generated from them with storm's CLI
(`go install github.com/gsoultan/storm/cmd/storm@v1.4.0`), and both are
committed. After changing a struct, against a scratch PostgreSQL database:

```sh
export STORM_DSN='postgres://postgres@localhost:5432/scratch?sslmode=disable'
storm diff <change_name> -out internal/store/migrations   # new migration
storm generate internal/store/db                          # query code
storm verify -stale internal/store/db                     # what CI checks
storm verify -pending -out internal/store/migrations
```

At start the server brings the database up to the structs with storm's
automigrate, in one transaction under an advisory lock, so several servers
starting together are safe. The files in `internal/store/migrations` are
the reviewed SQL for each change (embedded in the binary), and CI fails
when they or the generated code fall behind the structs.

Every query and every migration goes through storm; the server runs no
statement of its own. A type change PostgreSQL has no cast for (JSON once kept in
a text column becoming an array or jsonb) is declared on the column with
`ConvertFrom(storm.JSONText)`, and automigrate writes the conversion. The
server lets automigrate replace indexes and CHECK constraints, which hold
no data, and still refuses any plan that drops a table or column.
