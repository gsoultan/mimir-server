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
./bin/mimir-server -addr :8080 -data ./data
```

Open http://localhost:8080. A server started with no database opens
**first-run setup**, which asks for:

1. The one-time **setup token** the server prints in its log
   (`"setup_token"`); set `MIMIR_SETUP_TOKEN` (at least 16 characters) to
   choose it yourself. Every step that changes anything needs it, and wrong
   guesses are limited per address, so nobody who merely reaches the port
   can claim the server.
2. The **database**: host, port, username, password, database name and
   whether to use SSL, with a **Test connection** button. On the same
   screen, the **audit log database**: the same one, or a second database
   with its own fields.
3. The **secret key** (below): generate a new one, which the screen shows
   with a copy button so you can keep it safe, or give the key a database
   backup was made with.
4. **Log retention**: how long audit events and webhook deliveries are kept.
5. The **owner** account. A database that already has accounts keeps them,
   and you sign in with one of those instead.

Setup writes `<data>/mimir.json` (or the file `-config`/`MIMIR_CONFIG`
names) and the full server starts on the same port; from then on the server
starts from that file, and setup never opens again. The file holds database
passwords, so it is written atomically, readable only by the server's user
(mode 0600, in a 0700 data folder), and the server refuses to start from one
other users can read. The database URLs in it are also encrypted with the
secret key, so a copy of the file alone gives nothing away. A file you write
yourself may use `"env:NAME"` or `"file:/path"` for either URL to keep the
password out of it:

```json
{ "database_url": "env:MIMIR_DB", "log_database_url": "file:/run/secrets/mimir-log-db" }
```

For a scripted or SSH install, `mimir-server setup` does the same without
the browser; the owner's password comes from `MIMIR_OWNER_PASSWORD` or a file,
never a flag:

```sh
echo "$OWNER_PASSWORD" | ./bin/mimir-server setup -data ./data \
  -database-url 'postgres://mimir:secret@localhost:5432/mimir' \
  -owner-email you@example.com -owner-name 'Your Name' -owner-password-file -
```

`-database-url` (or `MIMIR_DATABASE_URL`) and `-log-database-url` (or
`MIMIR_LOG_DATABASE_URL`) still come first when set, so a container can skip
the file altogether. The server keeps its accounts, models, runs and
settings in PostgreSQL. On start it brings the schema up to date by itself
(only the first start or an upgrade changes anything), so the role needs
`CREATE` on the database. Pool options such as `pool_max_conns=32` may go
in the URL; the default is at most 16 connections. Model weights and
uploaded examples stay as files in the `-data` folder.

With a **log database** the audit log is written, read and trimmed there,
apart from the records it describes; events from before it was set stay in
the main database. **Download a backup** covers the main database only;
`mimir-server backup` (below) takes both.

Secrets the server keeps in the database (webhook signing secrets, dataset
and scoring connection strings, alert passwords and bot tokens, sign-in providers'
client secrets, and each person's two-step verification key) are
encrypted with
AES-256-GCM, so a copy or backup of the database does not reveal them. The
key is 32 random bytes as base64 or hex (`openssl rand -base64 32`), taken
from `MIMIR_SECRET_KEY`, else from the file `-secret-key-file` (or
`MIMIR_SECRET_KEY_FILE`) names, else from `<data>/secret.key`, which the
server makes on first start and logs. Secrets written by an older version
are encrypted in place on the next start. Keep the key with your backups
but apart from them: without it those secrets cannot be read, and the
server refuses to start with a key that does not open them. Every console
sharing a database needs the same key.

A server given its database by flag or environment but with no accounts
yet asks only for the token, the retention and the owner. Until the owner
exists, nobody without the token can claim the server. Then choose **Add a model**, pick the model type and upload a
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
  not on workers; workers can fine-tune them (see below).

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

### Fine-tune a language model on your own writing

**Fine-tune it** on a language model's page teaches it from your own
examples with LoRA: small trainable pairs next to its attention
projections learn, while the model's own weights stay as they are,
quantized or not. Upload either:

- plain text (`.txt`, `.md`), such as documents or notes, which it learns
  whole, in passages of about 2,000 characters split at paragraphs; or
- JSON Lines, one example a line, in the formats OpenAI's fine-tuning
  takes: `{"messages": [...]}` chats, of which it learns the assistant's
  answers, `{"prompt": ..., "completion": ...}`, of which it learns the
  completion, or `{"text": ...}` passages.

A file can be up to 64 MB and 100,000 examples; each example counts up to
its first 512 tokens. A run defaults to 3 passes of 4 examples a step at
a learning rate of 2e-4, rank 8 pairs scaled by 16/8, and keeps a tenth
of the examples aside to score on: its score is how many tokens of those
it writes as they go on. A run resumes from its last pass when it is
stopped.

The fine-tune is a model of its own, with the base model's chat settings,
that answers in **Try a model**, `PredictService` and the OpenAI API like
any language model. Its file is only the adapter, a few megabytes, stored
next to the base model; it names the base by its SHA-256, so it is never
applied to another, and the base can't be deleted while it has
fine-tunes. Fine-tuning a fine-tune continues from its adapter.

A run trains on a worker when one is connected, so a rented GPU box does
the heavy part (see Console and workers), as long as the base model's
file is under 1 GB, the most a worker takes in one message; a larger
model trains on the console. The fine-tune then runs on the console, as
language models do. In the API, a fine-tune is a `TrainingService.CreateJob`
of architecture `language-model` on a dataset of kind
`DATASET_KIND_TEXT`, with `base_model_id` naming the model to start from.
The adapter format is mimir-model's `spec/adapters.md`.

Anything else, such as an ONNX model with an operator outside that list,
a recurrent or quantized ONNX model, a GGUF file of another architecture
or with RoPE scaling, or a safetensors file, is refused when you add it,
with a message that names what isn't supported.

Uploads are limited to 256 MB by default, which fits ONNX models and small
language models. Start the console with `-max-model-mb 2048`, say, to take
bigger GGUF files. The console sends a file straight from disk and the
server writes it to disk as it arrives, then converts it from there, so
adding a model of any size takes about 65 MB of memory besides the file
pages the system caches; a 1.1 GB GGUF takes about 12 seconds.

From an app or a script, send a large file the same way: post it as the
body of `/api/uploads`, then name the `upload` it answers with in
`CreateModel` instead of sending `weights`. `weights` reads the whole file
into memory first, about five times its size for a large one.

```sh
id=$(curl -sS --data-binary @model.gguf -H 'Authorization: Bearer mimir_...' \
  localhost:8080/api/uploads | jq -r .upload)
curl localhost:8080/mimir.v1.ModelService/CreateModel \
  -H 'Authorization: Bearer mimir_...' -H 'Content-Type: application/json' \
  -d "{\"name\": \"My model\", \"architecture\": \"language-model\", \"upload\": \"$id\"}"
```

An upload needs a key or person that may add models, can be used once, by
whoever sent it, and is removed if it isn't used within an hour.

### On a server

[docs/deploy.md](docs/deploy.md) runs the console under systemd behind an
HTTPS reverse proxy, with nightly backups and upgrades you can undo; the
systemd units and the Caddy and nginx configs are in [`deploy/`](deploy).
Three commands help:

- `mimir-server backup -out /var/backups/mimir [-keep 14]` dumps the main
  database and the log database (with `pg_dump`) and archives the data
  folder into a new folder, with a manifest naming the version and the
  secret key's id. The key itself is left out.
- `mimir-server restore -from <backup>` puts one back into empty
  databases and data folder (with `pg_restore`), after checking the key is
  the one it was made with and the backup isn't from a newer version.
- `mimir-server migrate -check` prints the schema changes this version
  would make when it starts, and fails if it would refuse one;
  `mimir-server migrate` makes them without starting.

Each takes `-data`, `-config`, `-database-url`, `-log-database-url` and
`-secret-key-file` as the console does, and reads the configuration file
setup wrote.

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
  can only set it up until it's on. Turning it off, or setting up another
  authenticator, takes the password and a current code (or a recovery
  code), so a stolen cookie and password aren't enough. An admin can reset
  it for someone who lost their phone.
- **Sign in with Google, GitHub or a company account.** Under
  **Settings → Sign-in** an owner adds Google, GitHub, or any OpenID
  Connect provider (Microsoft Entra ID, Okta, Keycloak, Authentik and the
  like): the page shows the redirect address to register there, and takes
  the client id and secret it gives back. The sign-in page then has a
  "Continue with" button for each. Someone who already has an account
  signs in with the email address the provider checked, and is tied to
  that provider's id for them from then on, so a later change of address
  at the provider keeps the same account. Someone new gets an account only
  when one of the provider's rules matches their email domain or a group
  (a GitHub organization, or the groups claim of an OpenID Connect token),
  as a viewer, editor or admin in the rule's team; everyone else is turned
  away. Two-step verification still asks for its code after the provider,
  and the policy under **Settings → Security** still applies. The sign-in
  uses PKCE and a nonce, and is checked against a short-lived cookie set
  when it started, so a link can't sign you in to someone else's account.
- **Sessions.** Each browser gets its own session, listed under
  **Security** with a sign-out button and a "sign out everywhere else"
  button. A session ends after 30 days, or 7 days without use, and is tied
  to the browser that started it: a cookie replayed from a different
  browser ends the session instead of opening it. A new token is minted at
  every sign-in, after the second factor and after a password change;
  a password change, a smaller role or a disabled account signs the person
  out everywhere. When an admin sets someone else's password, that
  person's API keys are revoked too.
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
known model types in `internal/arch`: handwritten-digit networks
(`mnist-mlp`, `mnist-cnn`), table and photo networks (`table-mlp`,
`text-mlp`, `number-mlp`, `photo-cnn`, `photo-table`), and models fit to a
whole table at once with mimir's `tree` and `linear` packages: boosted
trees (`table-boost`, `number-boost`), random forests (`table-forest`,
`number-forest`) and logistic and linear regression (`table-linear`,
`number-linear`), support vector machines (`table-svm`, `number-svm`, on
up to 10,000 rows), nearest neighbours (`table-knn`, `number-knn`) and
naive Bayes (`table-bayes`). `table-auto` and `number-auto` try them all
with a range of settings, score each on rows held out by
cross-validation, and keep the best (mimir's `automl` package); they are
the default for tables.
Photos can also be read by an 18-layer ResNet (`photo-resnet`) or a
MobileNetV2 (`photo-mobilenet`) from mimir's `vision` package, and sounds
by `sound`, a small convolutional network over each second's log mel
spectrogram from mimir's `audio` package; a sound is asked about as a WAV
file in Predict's `sound` field. `find-things` finds things in photos, as
CenterNet does on a ResNet-18 of a quarter the usual width: each photo is
read whole at 128 by 128, and Predict answers with `boxes`, each a label,
a score and where it is as shares of the photo. It scores itself by its
mean average precision at an overlap of 0.5 on the photos held out. Five more
read a table's cells themselves and answer questions that aren't its rows:
`find-groups` sorts rows into groups by k-means with no answer column,
`odd-one` scores how unusual a row is with an isolation forest,
`recommend` lists the items a person is likeliest to want from who picked
what (the answer column is the item), and `forecast` says what comes next
for each series of a dated table, any number of days, hours, weeks or
months ahead (the answer column is the value), and `best-action` learns
from a log of what was tried and how it turned out which action is likely
to turn out best in a new case, with the reward it expects of each (the
answer column is the action, and a column such as reward, revenue or
converted says how it turned out). A fitted
model is saved as a Mimir Model file of buffers with its shape in the
metadata, so it is served, kept on devices and moved between workers like
a network. An upload names its type, and the server checks the weights
fit that type before storing them. To serve a model type of your own without changing
the server, write it in Go and run it on a worker (see Your own model
types).

**Models → Build a model** is where a new model starts. It asks what the
model should do (yes or no, pick a group, a number, find groups, write
or chat, recommend, forecast, spot the odd one, pick the best action, find it in a photo), then
ranks the ways to get there: a template, your own data, a model file
trained elsewhere, or your own code.

## Teach a new model

Under **Models → Teach a new model** (or **Training**), anyone who can add
models first says what their data looks like, then trains in three steps:

- **A table**: rows and columns from a spreadsheet or a database.
- **Text**: reviews, messages or notes, one per cell next to the answer.
- **Photos in folders**: one folder per answer, as a ZIP or a dropped folder.
- **Photos with details**: a ZIP with `data.csv` and the photos it names.
- **Photos with boxes**: a ZIP of photos and a `boxes.csv` with one row per
  thing in them: the photo, what it is, and its box in pixels (`left`,
  `top`, `width`, `height`, or `right` and `bottom`). A row with `what`
  empty is a photo with nothing in it.
- **Sounds in folders**: WAV recordings, one folder per answer, as a ZIP or
  a dropped folder. Each is heard as one second at 16 kHz, from its middle,
  whatever its rate and channels.

1. **The examples.** Upload a `.csv` with a header row and pick the column
   that holds the answer, upload photos, query a database, or use the
   built-in handwritten digits (MNIST, downloaded once into the data
   folder). Columns can hold numbers, categories (a word from a short
   list), yes/no, free text, or a photo's file name; the server tells
   them apart and turns each into numbers. CSVs go up to 64 MB, a million
   rows and a thousand columns; ZIPs up to 200 MB and 30,000 photos or
   20,000 sounds.
2. **What kind.** The model types that fit the examples, plus a name:
   a table or text classifier, a **number predictor** when the answer is
   a number, a **picture classifier** for photo folders, a **photo
   and table classifier** when each row has a photo too, and a **sound
   classifier** for sound folders, and a **finder** for photos with boxes. A table model
   can be Small, Medium (the usual) or Large, or **designed** step by
   step: up to 8 "learn patterns" (dense, 1 to 1024 units) and "forget a
   little" (dropout) steps, with how many numbers it learns and how many
   examples that wants shown as you go. The design is saved in the model
   file, so any server or worker rebuilds it the same way.
   A network can also **start from an earlier model** of the same type:
   it keeps what that model learned and learns the new answers on top
   (transfer learning), so a photo model for new folders needs fewer
   photos and less time. ResNet and MobileNet models get a new last layer
   for the new answers; other types start from the earlier weights only
   when the answers are the same, and otherwise learn from scratch, which
   the run's note says. The earlier model is left as it is.
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
read-only transaction (SQLite files open read-only, with `ATTACH` turned
off so a query can't reach other files), returns up to a
million rows, and can't point at the server's own data folder. The
connection string stays on the server and is never sent back.

Examples from a query can **keep learning** every hour, day or week. When
a pass is due the server runs the query again, then teaches the same
model further from where it left off. The model keeps its id, so apps
keep working, and it is only replaced when the new pass scores at least as
well on held-out rows; otherwise the run says it kept the previous
version, and the pass is kept as a version you can still choose. If the columns or answers changed, the pass learns from scratch.
The run page shows the schedule, the next pass and what each pass did,
and the schedule can be changed or turned off there.

A run can also **wait for new rows**: with "Wait for new rows" set to, say,
500, each look runs the query again and starts a pass only once it returns
at least 500 more rows than the last pass learned from; otherwise the run
page says how many new rows it found. Such a run may look as often as every
15 minutes, so a model follows a table that keeps filling up without
retraining on nothing. Due passes are claimed in the database, so several
servers never start the same one, and their queries run in the background,
at most two at a time, so a slow database never holds up the training
queue. Passes go to training workers like any other run, and the run page
lists every pass of the model with its rows, score and outcome.

Datasets and runs are also in the API (`DatasetService`, `TrainingService`
in `proto/mimir/v1/training.proto`), and webhooks can listen for
`training.queued`, `training.finished` and `training.failed`.

### Score a whole list

**Score a list** answers every row of a table with one model at once,
for when the questions come as a list rather than one at a time: this
month's customers, every open order. Pick a model that reads a row of a
table and where the rows are: a CSV file (up to 64 MB; it needs no answer
column), a table of examples, or, for admins, a database query or a
.csv file or folder in S3-compatible storage, read the same way as for
training. The rows are checked as the scoring is made: at least one of
their columns has to be one the model reads, and other columns come along
untouched, so an id column lets you match the answers up.

Scorings run in the background, two at a time, each asking the model 16
rows at once so it answers them in batches, on a worker when the model
runs on one. The page shows progress, the first rows and **Download
results**: a CSV of every row, in order, with the model's answer after
it: `predicted`, then `confidence` for a model that picks a label or
`suggestions` (the top ten, best first) for one that ranks items, and
`problem`, which says why a row couldn't be answered, such as a word in a
number column; one bad row never stops the rest. A scoring from a database
or storage can run again every hour, day or week, fetching its rows afresh
and replacing the results; the results of a run only replace the last
ones once it finishes. **Score again** runs it now. Scorings of a model
go with it when it is deleted.

The results download from `GET /api/scorings/{id}/results` with a session
or an API key that may add models, and the rest is in the API
(`ScoringService` in `proto/mimir/v1/scoring.proto`). Webhooks can listen
for `scoring.finished` and `scoring.failed`. Rows scored this way don't
count toward the prediction limits under Settings.

### Ask your documents

**Ask your documents** answers questions from a team's own files: handbooks,
policies, notes. Make a collection, pick the language model that answers
and the one that reads the documents (it makes an embedding of each
passage; it can't change later, as the passages were read by it), and drop
in files: PDF (with text, not scans), Word (.docx), Markdown, web pages
(.html) or plain text, up to 64 MB each. The server reads them in the
background: it cuts each into passages of a few paragraphs (a Markdown
passage starts with its heading) and keeps them in PostgreSQL with their
embeddings and a full-text index.

A question is matched two ways, by meaning (its embedding against every
passage's, held in memory for the four collections asked last; a
collection holds up to 50,000 passages) and by words (PostgreSQL's
full-text search, English stems), and the two rankings are merged by
reciprocal rank fusion. The best five passages go to the language model
with the collection's instructions and the rule to answer only from them,
citing each as [1], [2]; a question too long for the model with all five
is asked with fewer. The page streams the answer with each citation
linked to its passage, and a short follow-up is searched together with the
question before it. Nothing leaves the server.

Apps ask a collection through the OpenAI-compatible API by sending its
name (shown on its page, such as `shop-handbook`) as the model to
`/v1/chat/completions`; it is listed in `/v1/models`, and an API key needs
access to both of its models. The rest is in the API (`DocumentService`
in `proto/mimir/v1/document.proto`), including `Search`, which returns the
matching passages without an answer. Deleting the model that reads a
collection's documents deletes the collection.

### Why did it answer that

For a model taught on a table, the Try page has **Why this answer?** under
each answer. It shows how much each column moved the answer, compared with
a typical row (each number column at the value training filled gaps with,
each category at its most common value, the rest empty): for a category,
in points of the chance of the answer given; for a number, in the answer's
own units. The effects are Shapley values, so they add up to the
difference between this row's answer and the typical row's. Only the
columns that differ from the typical row take part; with up to ten of them
every combination is tried and the result is exact, and above that (up to
sixty) the server samples orderings, seeded by the row so the same
question gets the same explanation.

A model's page shows **Columns that matter**: the average size of each
column's effect over up to 24 rows, taken from its recent questions or,
when it has none, from the examples it learned from. In the API these are
`Explain` and `ExplainModel` on `PredictService`, and need permission to
ask the model. Models that rank, and models of photos, sound or text,
aren't explained.

### Versions

Every model keeps the weights it has had as numbered versions: version 1
is its upload or the run that taught it, and each keep-learning pass adds
one, including a pass that scored worse and so wasn't put live. The
model's page lists them, newest first, with where each came from, its
score and the run that made it; **Use this one** puts an earlier (or a
held-back) version live. The model keeps its id, so apps carry on, and the
next question is answered with that version's weights, by the console and
by workers alike. A model keeps its 20 newest versions, and always the
live one. The files sit under `models/versions/` in the data folder,
hard-linked to the live weights where the disk allows, so the live
version takes no extra space.

In the API, `ModelService.ListModelVersions` lists them and
`ModelService.UseModelVersion` puts one live; the switch is in the audit
log and sends a `model.updated` webhook with the version.

### How a model does in use

A held-out score says how a model did on examples it was trained with;
in use, the questions drift. Every prediction comes back with a
`predictionId`, and the server keeps the answer (with the question's
fields, not a photo) for 30 days. When your app learns what the right
answer was, it sends it back:

```sh
curl https://mimir.example.com/mimir.v1.PredictService/SendFeedback \
  -H 'Authorization: Bearer mimir_...' -H 'Content-Type: application/json' \
  -d '{"predictionId": "01a1...", "label": "yes"}'   # or "value": 42.5
```

The answer counts as right when the label matches, or a number is within
10% of the real one. Sending it again replaces it. The model's page shows
the last 7 days' answers and how many of the checked ones were right
against its score when trained, and a bar for each of the last 30 days.
When, over at least 20 checked answers in 7 days, a model gets more than
10 points fewer right than when trained, the server alerts owners by
email or Telegram and sends a `model.accuracy_dropped` webhook, at most
once a day per model; its versions and the keep-learning schedule are the
places to act. `ModelService.GetModelQuality` returns the same counts.
Answers are written in batches every few seconds; daily counts are kept
for 400 days.

## Use it from your app

Every method works over Connect (HTTP/JSON), gRPC and gRPC-Web. The API is
in `proto/mimir/v1/model.proto`.

From Go, [mimir-server-sdk](https://github.com/gsoultan/mimir-server-sdk)
wraps it: models, predictions, streamed chats, training runs, webhooks,
people and workers, with retries and paged lists
(`go get github.com/gsoultan/mimir-server-sdk`).

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

It serves `GET /v1/models`, `POST /v1/chat/completions`,
`POST /v1/completions` and `POST /v1/embeddings`. Completions take
`stream` (server-sent events, ending in `data: [DONE]`, and
`stream_options.include_usage`), `max_tokens` or
`max_completion_tokens`, `temperature`, `seed` and `stop` (up to four
strings). System and developer messages become the instructions. Tools,
images, `n` above 1 and log probabilities aren't supported and are refused
with a 400.

`/v1/embeddings` turns `input`, a string or a list of up to 256, into an
embedding each, for search and grouping: the mean of the model's last
hidden states over the text's first 512 tokens, scaled to length 1, so
the dot product of two says how alike they are. A fine-tune embeds with
its adapter. It takes `encoding_format` `float` or `base64`; `dimensions`
must be left out or match the model's width, and lists of tokens aren't
taken.

```sh
curl localhost:8080/v1/embeddings \
  -H 'Authorization: Bearer mimir_...' \
  -H 'Content-Type: application/json' \
  -d '{"model": "tiny-llama", "input": ["Where is my order?", "Shipping times"]}'
```

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
  the port can read them, and the console warns at start when it listens
  beyond loopback.

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

### Training machines and serving machines

Training is the heavy work: a run holds CPUs or a GPU for minutes, while a
prediction wants an answer in milliseconds. To keep one from slowing the
other, give each worker a job with `-tasks` (or `$MIMIR_WORKER_TASKS`):

- `-tasks all` (the default) takes predictions and training runs alike.
- `-tasks train` takes training runs only. The console sends it no
  predictions, and a model it trains is left on *Automatic*, so the
  serving workers answer it. Its page says **Teaches only**.
- `-tasks serve` answers predictions only. The console sends it no runs,
  and the teach wizard won't offer it. Its page says **Runs only**.

```sh
# on the GPU box
mimir-server -role=worker -tasks train -server https://mimir.example.com -token mimirw_...
# on each box that answers the API
mimir-server -role=worker -tasks serve -server https://mimir.example.com -token mimirw_...
```

No GPU of your own? Under **Settings → Workers**, **Rent a GPU for
training** has the console start a GPU on Modal, AWS EC2, Google Cloud,
RunPod, Lambda Cloud or Vast.ai when a training run waits, and remove it
a few minutes after the last one; you pay only while it runs. Each
provider also offers machines with 4 or 8 GPUs, which train a run on all
of them at once (see below). See
[Training on a rented GPU](docs/deploy.md#training-on-a-rented-gpu).

You can also decide this from the console: each worker's page under
**Settings → Workers** has a **What it takes** choice (*As started*,
*Runs and teaches*, *Teaches only*, *Answers only*) that applies at once
to a connected worker. Told to teach only, the models placed on it go back
to *Automatic*; told to answer only, the runs waiting for it do.

When a run finishes, the console hands the new weights to every worker
that answers for the model (and loads them itself when it answers)
before anyone asks, so the first prediction is as quick as the rest. A
worker keeps answering with the version it has until the new one is
loaded, so apps calling the API see no error and no pause when a model
is retrained. A worker that connects gets the models placed on it the
same way.

The console itself runs no models with `-role=console`, so the API stays
quick whatever the workers are doing. A `standalone` console answers
small models itself and, by default, trains runs no worker takes; the
**Training on this console** switch under **Settings → Workers** turns
that off so every run goes to a worker and training never shares the
machine the API is served from (`-train-here=false`, or
`MIMIR_TRAIN_HERE=false`, is the default before anyone has chosen). A run
with no worker to take it waits in the queue, and starts when one
connects.

### Where work runs

- **Each model has a "Runs on" choice** on its page: *Automatic* sends
  questions to the least busy worker that can run it, or pick one worker,
  such as the one with the GPU. A model on an offline worker answers with
  a clear "offline" error rather than moving elsewhere.
- **The teach wizard has a "Train on" choice.** A run placed on an offline
  worker waits for it without holding up other runs, and the model it
  makes runs on the same worker.
- Removing a worker puts its models and waiting runs back on Automatic.

### One run on several workers

A big run can be split between several smaller machines that teach.
Choose **Train each run on → Up to N workers** under **Settings →
Workers**; when at least two such workers are free, a run with no "Train
on" choice goes to as many as are free, up to N, and otherwise trains on
one as before. The run's page names every worker it trained on.

Every worker reads the whole dataset, so they all turn rows into numbers
the same way, then learns from its own share of the batches: with three
workers each takes every third batch, so an epoch takes a third of the
steps. Every 8 steps, and at the end of each epoch, the console averages
their weights and sends the average back, so they carry on from one
model. The first worker scores the model, reports progress and sends the
finished weights. If one worker drops out the others stop and the run goes
back in the queue, starting over; a split run keeps no checkpoints.

Measured on one machine with each worker a process of its own pinned to
one CPU (`MIMIR_GROUP_BENCH=1 go test -run TestGroupBench -v
./internal/server`):

| Run, 3 epochs | 1 worker | 2 workers | 3 workers |
| --- | --- | --- | --- |
| table-mlp, 200,000 rows: whole run | 8.4 s | 6.0 s (1.4×) | 5.2 s (1.6×) |
| table-mlp: an epoch | 2.1 s | 1.26 s (1.7×) | 0.88 s (2.4×) |
| photo-cnn, 2,000 photos: whole run | 15.0 s | 10.5 s (1.4×) | 8.3 s (1.8×) |

Accuracy stayed the same (98.9% and 98.8% on the table). The whole run
gains less than an epoch does because reading the dataset and scoring it
happen once per worker however many there are, so the longer the run,
the closer it gets to the per-epoch speed-up. Splitting a run makes it
faster; it doesn't make it smaller: each worker still holds the whole
model and dataset, about as much memory as one worker training alone.

The built-in model types train this way. A custom worker's handler takes
part when it implements `worker.GroupTrainer` and averages its weights
with `TrainTask.Group.Mean`.

### Every GPU in a machine

A worker or standalone server on a machine with several NVIDIA GPUs
trains each run on all of them at once, as PyTorch's
DistributedDataParallel does in one process: the model has a copy on each
GPU, every batch is split between them, and their gradients are summed
over NCCL into one step, so the run takes the same steps it would on one
GPU with the same batch size, only sooner. The batch is split, not
multiplied, so a GPU gets a share of each batch: raise the batch size
under the run's settings to keep several busy. `-gpus N` limits a run to
the first N GPUs (`-gpus 1` turns it off). One run holds the GPUs at a
time; a second run that starts meanwhile trains on the first GPU beside
it, as on a machine with one. The Workers page shows how many GPUs a
worker teaches on, the console sends a run to an idle worker with the
most GPUs first, and the run's page says how many it trained on.

It applies to the built-in neural networks, such as the table, photo and
sound models; models that fit in one go (trees, nearest neighbours and
the like), object finders, and fine-tunes of language models use one
device. It combines with *One run
on several workers*: each worker then trains its share on all its GPUs.
This hasn't yet been measured on real multi-GPU machines; the same code
path runs in the tests with two CPU devices standing in for the GPUs.

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

For a model type mimir can't build itself, such as one that calls your
own Go code or another library, write the model in Go with the `worker`
package and run it as a worker. **Build a model → Write your own model** walks through it:

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
- **Database**: engine, server and audit log database (passwords hidden),
  size and counts, plus
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

You need Go, [Bun](https://bun.sh), and either Docker or Podman running
or a PostgreSQL database of your own. Then:

```sh
make dev        # or ./scripts/dev.sh
```

Open http://localhost:5173. The web app reloads as you edit it, and the
server rebuilds and restarts whenever a Go or SQL file changes (a build
error is printed and the old server keeps running). Webhooks may point at
local addresses. Stop everything with Ctrl+C.

Without `MIMIR_DATABASE_URL`, the script starts PostgreSQL 16 in Docker or
Podman (container `mimir-dev-postgres` on port 55432) and stops it again
on Ctrl+C; the server creates its tables at start. To use a database of
your own, set `MIMIR_DATABASE_URL` to its PostgreSQL URL. Files go to
`data/dev` and the container's database to a volume, so both survive
restarts; `make dev-reset` deletes both to start over. On Windows, run it
in WSL.

| Setting | Default | What it does |
| --- | --- | --- |
| `MIMIR_DATABASE_URL` | a PostgreSQL container | The server's database |
| `MIMIR_DEV_DATA` | `./data/dev` | Data folder |
| `MIMIR_DEV_ADDR` | `localhost:8080` | Where the Go server listens |
| `MIMIR_DEV_ARGS` | | Extra server flags, such as `-role console` |
| `MIMIR_DEV_PG_PORT` | `55432` | Local port of the PostgreSQL container |

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
