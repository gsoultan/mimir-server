# Changelog

## Unreleased

- ResNet, MobileNet and sound models taught from a few dozen examples
  answer as well as they learned: their batch norms' statistics are
  recomputed after every epoch. A MobileNet taught from 60 photos of two
  colours had answered every photo alike (mimir#39).
- A model with columns can be asked with none filled in, as a forecast of
  one series is; it used to ask for a list of numbers instead.
- Built against mimir v0.4.1.

## v0.2.0

The first release with notes by feature. The v0.1.x tags carried the same
work as it landed; this is the version to deploy. It needs PostgreSQL 14 or
later and is built against mimir v0.4.0, mimir-model v0.3.0 and storm
v1.4.0.

### Workers

- Workers take training, serving or both. Each worker's page under
  **Settings → Workers** has **What it takes** (as started, runs and
  teaches, teaches only, answers only), applied at once to a connected
  worker; `-tasks train` or `-tasks serve` sets it when a worker starts. The console
  can be kept from training itself with **Training on this console**, so
  the machines that answer the API never train (#34).
- New weights reach serving workers as soon as a run finishes, before
  anyone asks for them, and predictions wait in a queue when every worker
  is busy instead of failing (#37).
- One run trains on several workers together: **Train each run on** picks
  one worker, or up to 2, 3, 4, 6, 8 or 16. Each member learns from its
  share of the batches, and the run's page names every worker (#42).
- A prediction whose worker restarts or drops off is asked of another
  worker instead of failing, and deleting a model (or moving it to another
  worker) frees it on the workers that held it (#46).

### Rented GPUs

- A training run that no connected worker takes can rent a GPU by the
  minute from Modal, AWS EC2, Google Cloud, RunPod, Lambda Cloud or
  Vast.ai, under **Settings → Workers → Rent a GPU for training**. The machine runs a
  worker, and the console removes it once the run is done, when it passes
  **Longest per machine**, or when renting is turned off (#38). See
  [Training on a rented GPU](docs/deploy.md#training-on-a-rented-gpu).
- A live test rents one real machine from a provider and checks it starts,
  is found, and is gone after stopping. It is built only with the `livegpu`
  tag, so `go test ./...` never rents anything (#45).

### Model types

- Tables can be taught boosted trees, random forests and logistic or
  linear regression (`table-boost`, `number-boost`, `table-forest`,
  `number-forest`, `table-linear`, `number-linear`) (#53), support vector
  machines, nearest neighbours and naive Bayes (`table-svm`, `number-svm`,
  `table-knn`, `number-knn`, `table-bayes`) (#58). `table-auto` and
  `number-auto` try them all with a range of settings and keep the best;
  they are the default for tables (#57).
- From a table with no code: **Find groups** (`find-groups`), **Spot the
  odd one** (`odd-one`), **Recommend** (`recommend`, from who picked
  which item) and **Forecast over time** (`forecast`, per series and in
  total) (#55), and **Pick the best action** (`best-action`, from a log
  of what was tried and how it turned out) (#58).
- Photos can be read by a ResNet-18 (`photo-resnet`) or MobileNetV2
  (`photo-mobilenet`), and a run can **start from an earlier model** of
  the same type, keeping what it learned (#57).
- **Sounds in folders** teach a sound classifier (`sound`), and **photos
  with boxes** teach `find-things`, which answers with a box around each
  thing it finds (#58).

### Versions, quality and comparing runs

- Every model keeps its last 20 versions: the run or upload that made it
  and each keep-learning pass, including one held back for scoring worse.
  **Use this one** puts an earlier version live under the same model id
  (#50).
- Every prediction returns a `predictionId`; apps send the right answer
  back with `SendFeedback`, the model's page shows how it does in use
  over 7 and 30 days, and a model that drops more than 10 points sends an
  alert and a `model.accuracy_dropped` webhook (#54).
- **Compare** puts two to six runs side by side, every finished run has a
  full report (per-label scores and the confusion table, or the average
  miss and R² for a number), and **benchmarks** freeze a test from a
  dataset that every model takes the same way, with a leaderboard (#49).

### Language models

- **Fine-tune it** teaches a language model from your own text or JSON
  Lines with LoRA, as a new model that is only the adapter, and
  `/v1/embeddings` serves embeddings in OpenAI's format (#52).

### Training

- **Keep learning** can wait for new rows: a model taught from a database
  starts a pass only once its query returns a set number of new rows,
  looking as often as every 15 minutes. Due passes are claimed in the
  database so several consoles never start the same one, and queries run
  in the background so a slow database never holds up training (#41).
- Training a large table is faster and uses far less memory: the CSV is
  read twice instead of holding every cell, numbers are parsed once and
  text words hashed without copying (#31, #35).
- New table models default to two layers of 256 units; models trained
  before keep their shape (#32).
- `-bf16` (or `MIMIR_BF16=true`) uses bfloat16 matrix products on Intel
  AMX, about 1.6 times faster on a deep table model (#33).
- When memory runs short, a run slows down instead of stopping; only when
  memory stays critical for a minute is it put back in the queue to resume
  from its checkpoint. The console and workers stay up (#40).

### First-run setup and operations

- A server started without a configuration opens **first-run setup**: the
  setup token from the log, the database and the audit log database (with
  **Test connection**), the secret key, log retention and the owner
  account (#25).
- `mimir-server backup`, `restore` and `migrate` (with `migrate -check` to
  see the schema changes before an upgrade), systemd units, Caddy and nginx
  configs, and a [deploy guide](docs/deploy.md) (#27). The release archives
  now include `deploy/` and `docs/`.
- Large model uploads stream to disk (`POST /api/uploads`): a 1.1 GB GGUF
  takes about 65 MB of memory to convert (#30). Uploads that stall are cut
  off, so they can't hold every upload slot (#36).
- `mimir-load` load-tests the whole stack, and can soak it with workers,
  training runs and worker kills; [docs/capacity.md](docs/capacity.md) has
  what one console handles (#28, #29, #46).
- Shutdown is fast with workers connected (#23).

### Security

- **Sign in with Google, GitHub or a company account** (any OpenID
  Connect provider), added under **Settings → Sign-in**; new people get
  an account only when a rule matches their email domain or group (#51).
- Two-step verification keys are encrypted at rest, and the remaining
  security-pass items are closed (#26).
- Closed gaps in outbound connections and two-step verification
  guessing: ten wrong second-step codes hold the account back for 15
  minutes (#24).

### Clients

- [mimir-server-sdk v0.1.0](https://github.com/gsoultan/mimir-server-sdk),
  the Go client: models, predictions, streamed chats, datasets, training
  runs, webhooks, people, workers and rented GPUs.

### Dependencies

- mimir v0.4.0 (#43, #44, #47, #59), mimir-model v0.3.0 (#56), storm
  v1.4.0 (#20).

### Upgrading from v0.1.x

Back up first, then run `mimir-server migrate -check` with the new binary
to see the schema changes, swap the binary and restart; the console
migrates its database when it starts. Upgrade workers right after the
console. Workers older than this release keep working but don't free
deleted models.
