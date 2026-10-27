# Running mimir-server in production

This guide sets up one console on a Linux machine with systemd: PostgreSQL
for its records, HTTPS from a reverse proxy in front of it, nightly
backups, and upgrades you can undo. Workers on other machines are optional.
The files it uses are in [`deploy/`](../deploy).

## What you need

- A Linux machine with systemd. The release binaries need glibc.
- PostgreSQL 14 or later, on the same machine or another.
- The PostgreSQL client tools (`pg_dump`, `pg_restore`; the
  `postgresql-client` package) on the console's machine, the database's
  major version or newer, for backups.
- A DNS name for the console, such as `mimir.example.com`, and
  [Caddy](https://caddyserver.com) or nginx for HTTPS.

## 1. Install

```sh
useradd --system --home-dir /var/lib/mimir --shell /usr/sbin/nologin mimir
tar xzf mimir-server_v0.2.0_linux_amd64.tar.gz
install -m 0755 mimir-server_v0.2.0_linux_amd64/mimir-server /usr/local/bin/

install -d -m 0750 -g mimir /etc/mimir
install -m 0640 -g mimir deploy/systemd/mimir.env /etc/mimir/mimir.env
install -m 0644 deploy/systemd/mimir-server.service /etc/systemd/system/
systemctl daemon-reload
```

The unit runs the console as `mimir`, listening on `127.0.0.1:8080` only,
with its data folder at `/var/lib/mimir` (model weights, uploaded examples,
the configuration file and, by default, the secret key). Settings that
aren't flags in the unit go in `/etc/mimir/mimir.env`; every flag with an
environment variable can be set there (`mimir-server -h` lists them). The
unit is hardened (no capabilities, read-only system, private `/tmp` and
devices); on a machine that runs models on a GPU, remove `PrivateDevices`.

## 2. Make the database

```sh
sudo -u postgres createuser --pwprompt mimir
sudo -u postgres createdb --owner mimir mimir
```

The console's role needs `CREATE` on the database: it creates and updates
its own tables at start. To keep the audit log apart (its own disk, its own
retention, or access for an auditor), make a second database the same way
and give it on setup's Database step.

## 3. Decide where the secret key lives

The console encrypts the secrets it stores (webhook signing secrets,
database connection strings for teaching, alert passwords, two-step
verification keys) with a 32-byte secret key. A copy of the database is
useless without the key, and so is a backup: **keep a copy of the key
somewhere other than the backups**, such as a password manager. The console
refuses to start with a key that doesn't open what it stored.

- **In the data folder (the default).** Setup's Secret key step generates
  one, shows it once with a copy button, and saves it as
  `/var/lib/mimir/secret.key`. Backups leave it out.
- **Outside the data folder**, so disk snapshots of `/var/lib/mimir` don't
  carry it either:

  ```sh
  openssl rand -base64 32 | install -m 0640 -g mimir /dev/stdin /etc/mimir/secret.key
  echo 'MIMIR_SECRET_KEY_FILE=/etc/mimir/secret.key' >> /etc/mimir/mimir.env
  ```

  The file must exist before the first start; setup then uses it rather
  than making one.

When you restore onto a new machine, put the old key back the same way, or
paste it into setup's Secret key step ("use a key I have").

## 4. Put HTTPS in front

Run the proxy on the same machine and tell the console to believe the
client address and `https` it forwards, in `/etc/mimir/mimir.env`:

```sh
MIMIR_TRUSTED_PROXIES=127.0.0.1,::1
```

Without that the console sees every request as plain HTTP from
127.0.0.1: sign-in cookies lose `Secure`, there's no HSTS, rate limits
count everyone as one address, and workers on other machines are refused
as unencrypted.

**Caddy** gets and renews the certificate itself. Use
[`deploy/caddy/Caddyfile`](../deploy/caddy/Caddyfile) with your name in it.

**nginx**: use [`deploy/nginx/mimir.conf`](../deploy/nginx/mimir.conf),
with a certificate from certbot or elsewhere. Either way the proxy must:

- **Speak HTTP/2 to the console for gRPC.** Workers link to the console
  over gRPC, as do apps that use gRPC instead of Connect or JSON. Caddy's
  config sends everything over HTTP/2; the nginx config sends requests
  whose `Content-Type` is `application/grpc…` to `grpc_pass` and the rest
  to `proxy_pass`, through two upstreams (nginx can't share kept-alive
  connections between the two).
- **Not time out a worker's link.** It stays open for as long as the
  worker runs and can go quiet from the console's side, so the gRPC read
  and send timeouts are a day.
- **Not buffer responses.** Chat answers, `GenerateStream` and the
  OpenAI-compatible API's `"stream": true` arrive word by word.
- **Take uploads** at least as large as `-max-model-mb` (256 MB by
  default), at `/api/uploads` as well as the RPCs, and pass them on as
  they arrive rather than buffering them first. Both configs do.
- **Keep `/metrics` private**, or set `MIMIR_METRICS_TOKEN`.

Both configs were tried with a worker linking through the proxy; the
console logs such a worker with `"security":"proxy"`.

## 5. Start it and run first-run setup

```sh
systemctl enable --now mimir-server
journalctl -u mimir-server -o cat | grep setup_token
```

Open `https://mimir.example.com/` (through the proxy, since you'll type a
database password) and follow setup: the one-time setup token from the
log, the database (and the audit log database, if separate), the secret
key, log retention and the owner account. Setup writes
`/var/lib/mimir/mimir.json`, readable only by `mimir`, and the full console
starts on the same port. To choose the token yourself, set
`MIMIR_SETUP_TOKEN` (16 characters or more) in `mimir.env` before the first
start, and remove it afterwards.

Over SSH instead of the browser:

```sh
echo "$OWNER_PASSWORD" | sudo -u mimir mimir-server setup -data /var/lib/mimir \
  -database-url 'postgres://mimir:secret@localhost:5432/mimir' \
  -owner-email you@example.com -owner-name 'Your Name' -owner-password-file -
```

Run it as `mimir` so the files it writes belong to the service.

Check it: `curl -fsS https://mimir.example.com/readyz` answers `ok` when
the database does.

## 6. Add workers (optional)

On each worker machine, install the binary and the `mimir` user as above,
then:

```sh
install -d -m 0750 -g mimir /etc/mimir
printf 'MIMIR_CONSOLE=https://mimir.example.com\nMIMIR_WORKER_TOKEN=%s\n' "$TOKEN" \
  | install -m 0600 /dev/stdin /etc/mimir/worker.env
install -m 0644 deploy/systemd/mimir-worker.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now mimir-worker
```

The token comes from **Settings → Workers → Add a worker**. The worker
connects out to the console; it needs no open ports.

### Keeping training off the machines that answer

A worker takes predictions and training runs alike unless you tell it
which: `train` for training runs only, `serve` for predictions only. Set
it where the worker starts (`-tasks`, or `MIMIR_WORKER_TASKS` in
`worker.env`) or from the console, under **What it takes** on the
worker's page, which applies at once and wins over the flag. Three
shapes, from one machine up:

1. **One machine.** The unit from step 1 as it is (`-role standalone`):
   the console answers predictions itself and trains runs itself, two at
   a time (`MIMIR_TRAINING_JOBS`). A training run and the API share the
   CPUs.
2. **Console plus workers.** Keep the console on its machine and add
   workers with `-tasks all`. Turn off **Training on this console** under
   **Settings → Workers** (or set `MIMIR_TRAIN_HERE=false` in `mimir.env`
   as the default) so the console never trains itself: it still answers
   small models from its own process, and every run goes to a worker. Or
   run it as `-role console` (edit `ExecStart` in the unit) so it runs no
   models at all and only coordinates.
3. **Training and serving apart.** The console as in 2, one or more
   workers with `MIMIR_WORKER_TASKS=train` on the machines with the cores
   or the GPU, and one or more with `MIMIR_WORKER_TASKS=serve` close to
   the API's callers. The console sends runs only to the training workers
   and predictions only to the serving ones, and a model trained on a
   training worker is left on *Automatic*, so the serving workers answer
   it. Scale either side by adding machines: each training worker takes
   as many runs at once as its `-capacity` (the CPU count by default),
   and `MIMIR_TRAINING_JOBS` on the console caps the total.

A model that finishes training reaches the serving workers on its own:
the console hands them the weights as soon as the run ends, and they
switch to the new version once it is loaded, answering with the old one
until then, so the API sees no error and no slow first request.

Whatever the shape, a run with no worker to take it waits in the queue
and starts when one connects, and a worker that stops hands its runs
back to the queue to resume from their checkpoints. Each worker's page
under **Settings → Workers** says what it takes (*Teaches only*, *Runs
only*, or *Runs and teaches*); the teach wizard only offers workers that
train, and a model's **Runs on** choice only offers workers that answer.

Language models (chat) are the exception: they run on the console
itself, not on workers, so a console that serves them needs memory for
them (see step 9) whatever the workers do.

### Training on a rented GPU

Instead of keeping a GPU machine of your own, the console can rent one by
the minute, only while a run trains, from [Modal](https://modal.com),
[AWS EC2](https://aws.amazon.com/ec2/),
[Google Cloud](https://cloud.google.com/compute),
[RunPod](https://www.runpod.io), [Lambda Cloud](https://lambda.ai) or
[Vast.ai](https://vast.ai). When a training run waits that nothing
connected takes, the console starts a machine with the GPU you chose,
running this same mimir-server as a worker that teaches only
(`-tasks train`). It trains the run on the GPU, waits a few minutes for
more, and stops; the console then removes the machine, so nothing is left
running or billing. The model it made is answered by your serving
workers, which get the new weights with the first question after it was
trained.

**Set it up**

1. Get credentials from the provider (see the table below).
2. Under **Settings → Workers**, in **Rent a GPU for training**, turn on
   **Start a GPU when a training run waits**, pick the provider under
   **Rent from**, paste the credentials, pick a GPU (and a region or zone
   where it asks) and check **This console's address**. Save.
3. Make sure runs go to it: turn off **Training on this console** (or run
   the console as `-role console`), so runs placed on *Automatic* that no
   other worker takes wait for the GPU, or pick **Rented GPU** under
   **Train on** when you start a run.

| Provider | Credentials | Also asks for | The machine |
| --- | --- | --- | --- |
| Modal | API token (`ak-…` and `as-…`) from **Settings → API tokens**; or leave empty and set `MODAL_TOKEN_ID`/`MODAL_TOKEN_SECRET` for the console | | a sandbox from NVIDIA's CUDA 12.6 image; Modal ends it at **Longest per machine** |
| AWS EC2 | access key of an IAM user allowed `ec2:RunInstances`, `ec2:CreateTags`, `ec2:DescribeInstances`, `ec2:DescribeImages` and `ec2:TerminateInstances`; or leave empty for the console's own AWS credentials | region | an on-demand instance from AWS's *Deep Learning Base OSS Nvidia Driver GPU AMI (Ubuntu 22.04)* with a 100 GB disk, in the region's default VPC; it terminates itself when it powers off |
| Google Cloud | JSON key of a service account with **Compute Instance Admin (v1)**; the machine starts in its project | zone | a VM from the Deep Learning VM image `common-cu129-ubuntu-2204-nvidia-580` with a 100 GB disk, on the `default` network; Google deletes it at **Longest per machine** |
| RunPod | API key from **Settings → API Keys** | | a Secure Cloud pod from NVIDIA's CUDA 12.4 image |
| Lambda Cloud | API key from **API keys** in the dashboard | region, and the name of an SSH key added there (Lambda requires one; the console never uses it) | an instance with Lambda Stack |
| Vast.ai | API key from **Account → Keys** | | the cheapest verified on-demand offer of that GPU, running NVIDIA's CUDA 12.4 image |

AWS gives new accounts no quota for GPU instances: before the first
start, ask for **Running On-Demand G and VT instances** of at least 4
vCPUs under **Service Quotas → Amazon EC2**. Google Cloud has a similar
GPU quota per region, and Lambda Cloud and Vast.ai sometimes have no
machine of a GPU free; the card then says so and the console tries again
later.

Saving adds a worker called **Rented GPU** to the list. Its card says
*Asleep* until a run waits; then it starts (from under a minute on Modal
to a few minutes for a VM to boot), shows as online while it trains, and
goes back to sleep. **Stop it now** stops it at once; a run it was
training goes back in the queue and resumes from its checkpoint. If it
can't start or never joins within 15 minutes, the console removes the
machine, the card says why, and the console tries again after a minute,
then less often.

The console removes a machine when its worker has stopped (after
**Stop after idle**) and stayed away for 3 minutes, when it has run past
**Longest per machine**, or when you turn renting off or pick another
provider. The VMs (AWS, Google Cloud, Lambda) also power themselves off
when the worker stops and at **Longest per machine**, in case the console
can't reach the provider then.

What it needs:

- **The console reachable from the internet over https**, at the address
  you give, through the proxy from step 4 or `-tls-auto`. The rented
  machine connects out to it, like any worker. A console on a private
  network can't use this.
- **The program.** The machine downloads mimir-server from the console
  itself (it shows the worker's token for it), so both run the same
  version. That works when the console runs on linux/amd64, as the
  release does. Otherwise give a **Download address** for the linux/amd64
  build of the same version. The proxy must pass `/api/workers/program`
  (both configs in `deploy/` do; it is about 80 MB).
- **Its identity.** The console makes the worker's token and key itself
  and keeps them, with the provider's credentials, encrypted with its
  other secrets, so every machine it starts is the same worker. The
  machine gets them in its environment (on a VM, in a file only root can
  read), so anyone who can read your instances' settings at the provider
  can read them too. To replace them, remove the **Rented GPU** worker
  (which turns renting off) and turn it on again.

What it costs: the provider bills the time the machine runs, from start
to removal. An hour of each GPU when this was written:

| Provider | GPUs and prices an hour |
| --- | --- |
| Modal | T4 $0.59, L4 $0.80, A10 $1.10, L40S $1.95, A100 40 GB $2.10, A100 80 GB $2.50, H100 $3.95, H200 $4.54, B200 $6.25, plus about $0.24 for the CPU core and 4 GB of memory (Modal charges a sandbox's at three times its usual rate) |
| AWS EC2 (us-east-1) | T4 (g4dn.xlarge) $0.53, L4 (g6.xlarge) $0.81, A10G (g5.xlarge) $1.01, L40S (g6e.xlarge) $1.86 |
| Google Cloud (us-central1) | T4 (n1-standard-4) $0.54, L4 (g2-standard-4) $0.71, A100 40 GB (a2-highgpu-1g) $3.67, A100 80 GB (a2-ultragpu-1g) $5.07 |
| RunPod (Secure Cloud) | RTX A5000 $0.27, L4 $0.43, RTX A6000 $0.49, RTX 4090 $0.69, L40S $0.86, A100 80 GB $1.64, H100 $2.69 |
| Lambda Cloud | A10 $0.75, RTX A6000 $0.80, A100 40 GB $1.29, H100 $2.49 |
| Vast.ai | a market: often RTX 3090 $0.20, RTX 4090 $0.35, RTX A6000 $0.45, L40S $0.70, A100 80 GB $1.00, H100 $1.90 |

So a 10-minute run on an L4 with the default 5 minutes of waiting costs
about 15 minutes of the machine, or about $0.20 on Modal or AWS; a VM
also takes a few minutes to boot. Modal's free Starter plan includes $30
of use a month. Tables, text and small photo sets train well on a T4, L4
or A10; large photo sets are where an A100 or H100 pays off. **Longest
per machine** (6 hours by default, at most 24) caps what one machine can
cost; a run still going then resumes on a fresh machine. Each machine is
named `mimir-worker-` and the start of the worker's id at the provider
(on Modal, it runs under the app `mimir-worker`), where its logs and
spending are.

## 7. Backups

A backup is three things, and `mimir-server backup` takes all of them:

- the **main database** (accounts, models, runs, settings),
- the **audit log database**, when it has its own,
- the **data folder**: model weights, uploaded examples, training
  checkpoints and the certificate `-tls-auto` made. The secret key and
  `mimir.json` are left out.

```sh
install -d -o mimir -g mimir -m 0700 /var/backups/mimir
install -m 0644 deploy/systemd/mimir-backup.service deploy/systemd/mimir-backup.timer /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now mimir-backup.timer
```

The timer backs up every night at about 03:15 into a new folder such as
`/var/backups/mimir/mimir-20261022-031502/` and keeps the newest 14. Run
one now with `systemctl start mimir-backup`, or by hand:

```sh
sudo -u mimir mimir-server backup -data /var/lib/mimir -out /var/backups/mimir -keep 14
```

It runs while the console does: the databases are dumped with `pg_dump`
(each one consistent in itself), then the data folder is archived. Each
folder holds `main.dump`, `log.dump` (if any), `files.tar.gz` and a
`manifest.json` naming the version, schema and an id of the secret key (not
the key). Copy the folders off the machine; they hold password hashes and
your models, so treat them like the database itself.

**Settings → Database → Download a backup** in the web app gives a dump of
the main database only, for a quick copy.

### Restoring

Restore into empty databases and an empty data folder, with the console
stopped. On a new machine, follow steps 1 to 4 first (install, make the
databases, put the key back), then:

```sh
systemctl stop mimir-server
sudo -u mimir mimir-server restore -from /var/backups/mimir/mimir-20261022-031502 \
  -data /var/lib/mimir \
  -database-url 'postgres://mimir:secret@localhost:5432/mimir' \
  -log-database-url 'postgres://mimir:secret@localhost:5432/mimir_log'   # if the backup has one
```

Before it writes anything, restore checks that the key is the one the
backup was made with, that the backup isn't from a newer version than the
binary, and that the databases have no tables and the data folder none of
the backup's files. Each database is restored in one transaction, so a
failure leaves it as it was. `-replace` restores over what is there
instead, for a backup made by the version you're running.

Then start the console. On a new machine with no `mimir.json`, setup
opens: give it the same databases (it finds the accounts and skips the
owner step) and the key. An older backup is brought up to the binary's
schema on start, as an upgrade would be.

Try a restore onto a spare machine or database now and then; a backup you
haven't restored is a guess.

## 8. Upgrades

The console updates its own database schema when it starts:

- Changes are planned from the model the binary carries and applied **in
  one transaction**: the schema moves to the new version or not at all.
- An **advisory lock** makes consoles that start together migrate once.
- A change that could **lose data** (dropping a table or column, narrowing
  a type) is never applied on its own: the console stops with the change
  it refused instead.

To upgrade:

```sh
# 1. Back up first; it is your way back.
systemctl start mimir-backup

# 2. See what the new version will change, without changing it.
sudo -u mimir /tmp/mimir-server-new migrate -check -data /var/lib/mimir

# 3. Swap the binary and restart.
install -m 0755 /tmp/mimir-server-new /usr/local/bin/mimir-server
systemctl restart mimir-server
journalctl -u mimir-server -o cat | grep '"database ready"'   # migration_steps says how many changes ran
```

`migrate -check` prints the SQL each database would take (`up to date`
when nothing changes) and fails if the console would refuse a change.
`mimir-server migrate` without `-check` applies the changes without
starting the console, if you'd rather migrate as a separate step.

Upgrade workers right after the console: each worker's page under
**Settings → Workers** shows the version it runs.

### Rolling back

If the new version's `migrate -check` said **up to date**, the schema
didn't change: put the old binary back and restart.

If the schema changed, the old version won't start on the new schema; it
would have to drop what it doesn't know, and it refuses to. Roll back by
restoring the backup from step 1, made by the old version, into empty
databases:

```sh
systemctl stop mimir-server
install -m 0755 /tmp/mimir-server-old /usr/local/bin/mimir-server
sudo -u postgres dropdb mimir && sudo -u postgres createdb --owner mimir mimir
# the same for the audit log database, if it has its own
sudo -u mimir mimir-server restore -replace -data /var/lib/mimir \
  -from /var/backups/mimir/mimir-20261022-031502
systemctl start mimir-server
```

`-replace` here lets the data folder's files be put back over the ones
there; the databases are empty. (Restoring over the newer database
without recreating it leaves the tables only the newer version has, and
restore stops and says so.)

Anything written since the backup (new models, runs, audit events) is
lost, so roll back soon or not at all.

## 9. How much one console handles

Measured on a 4-core, 15 GB VM with PostgreSQL and the load generator on
the same machine; [capacity.md](capacity.md) has the details and how to
rerun it with `cmd/mimir-load`.

| Workload | One console (4 cores, 15 GB) |
| --- | --- |
| Predictions per second, small models | about 2,800 a second from 32 callers on; p99 under 50 ms up to 64 callers |
| Chat: concurrent streaming answers, 1B language model | about 2 tokens a second in all; up to 4 answers per model at once, the rest wait |
| Training runs alongside requests | 2 at once by default, about 1 epoch a second each on a 20,000-row table; predictions keep 2,200 a second beside them |
| Teams and people active at once | 32 teams with a signed-in admin each and 64 API callers: no errors, console pages p99 141 ms |
| Database connections needed | at most 11 on the main database and 2 on the log database; the default pool of 16 is enough |

Memory: 40 MB idle, under 250 MB serving small models, about 3 GB with a
1B language model loaded, and about 65 MB more while a model of any size
is uploaded and converted. Language models are the first limit: run them on
workers. `-training-jobs` and `-model-cache-mb` bound the rest.

## 10. Keeping an eye on it

- `GET /healthz` (the process serves) and `GET /readyz` (the databases
  answer) for probes and uptime checks.
- `GET /metrics` for Prometheus: request rates, errors and latency by
  route and procedure, prediction batches and queue time, models loaded.
- `journalctl -u mimir-server`: JSON lines, each request with a
  `request_id`, which the response's `X-Request-Id` header also carries.
- `systemctl list-timers mimir-backup.timer` and
  `journalctl -u mimir-backup` show when backups last ran and how they
  went.
