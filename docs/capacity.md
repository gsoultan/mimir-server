# Capacity of one console

Measured with `cmd/mimir-load` against the release build (v0.1.0 plus the
fixes in #28), standalone role, on one 4-core Intel Xeon VM at 2.8 GHz with
15 GiB of memory. PostgreSQL 16 (default settings, 100 connections), the
main and audit log databases, and the load generator all ran on the same
machine, so every figure here is what the console gets with a quarter to a
third of the CPU taken by others. A dedicated machine does better. Cloud
VM timings varied by about 15% between identical runs.

## Predictions

Two small image models per team (`mnist-mlp`, `mnist-cnn`), API keys, a
caller sending its next request as soon as the last is answered:

| callers at once | predictions/s | p50 | p90 | p99 |
| ---: | ---: | ---: | ---: | ---: |
| 8 | 1,680 | 4 ms | 9 ms | 15 ms |
| 32 | 2,300–2,800 | 11–12 ms | 20–25 ms | 29–47 ms |
| 64 | 2,850 | 21 ms | 32 ms | 46 ms |
| 128 | 2,800 | 44 ms | 58 ms | 78 ms |

Throughput levels off at about **2,800 predictions a second** from 32
callers on; more callers only add queueing time. The server uses 2 of the 4
cores at that point, and PostgreSQL about one: each prediction still reads
the key, its user and the model (three queries, no writes).

**Many teams:** 32 teams, each with an admin signed in to the console
(listing models and runs every second) and 64 prediction callers in all:
2,900 predictions/s at p99 45 ms, console pages at p50 68 ms and p99
141 ms, no errors. RSS 181 MB.

## Training alongside requests

Training a table model (`table-mlp`, 20,000 rows, 18 columns): about
**1.1 epochs a second per job** with two jobs at once and nothing else.
By default a standalone console runs 2 jobs at once and queues the rest
(`-training-jobs`). With 2 jobs learning, 16 prediction callers still got
2,200 predictions/s at p99 22 ms; training slows rather than predictions.

On a CPU with Intel AMX (Xeon from Sapphire Rapids on), `-bf16` (or
`MIMIR_BF16=true`) multiplies matrices in bfloat16 with float32 sums.
A deep table model (layers of 1024, 1024, 512 and 256 units, 500,000 rows)
trained 1.6 times faster, 70 s instead of 111 s for 3 epochs, at 99.2%
accuracy instead of 99.3%. Without AMX the flag changes nothing.

## Chat

A language model the size of TinyLlama 1.1B (22 layers, Q8_0, 1.1 GB file,
random weights; the speed does not depend on the values), answers of 64
tokens:

| chats at once | tokens/s, all together | first token p50 | whole answer |
| ---: | ---: | ---: | ---: |
| 1 | 1.1–1.8 | 1.1 s | 37 s |
| 4 | 2.1 | 5.7 s | 78 s |
| 2, with predictions and training | 1.4 | 2.6 s | 70 s |

This is the limit that matters: **one console streams a 1B model's
answers at about 2 tokens a second in total**, so it suits a few people
trying a model, not a chat product. Up to four answers per model are
written at once and the rest wait their turn. The same file runs at 14
tokens/s in `mim-run` from mimir-model; the gap is in mimir's runtime,
which rewrites the whole key-value cache for every token (see below).
Run language models on workers to keep them off the console.

## Memory

| state | RSS |
| --- | ---: |
| idle, no models loaded | 40 MB |
| predictions, 2–64 small models loaded | 100–250 MB |
| 1B language model loaded, chatting | 2.7–3.1 GB |
| uploading a 1.1 GB GGUF (converted on upload) | 65 MB heap, plus 1.1 GB of cached file pages |

The console sends a model to `/api/uploads`, which writes it to disk as it
arrives; the server then converts it from a memory-mapped file to a new
file on disk and moves that into place. Memory the server allocates stays
at about 65 MB whatever the file's size. The file's pages count towards
its RSS while it is read (1.2 GB peak here), but they are the system's
file cache, which it drops again whenever it needs the memory.

| uploading the 1.1 GB GGUF | before | after |
| --- | ---: | ---: |
| time, upload to model ready | 85–102 s | 12 s |
| server memory, peak | 7.5 GB | 65 MB heap (1.2 GB RSS with file cache) |
| server CPU | 149 s | 2.3 s, plus 10 s of kernel time writing to disk |

On the machine measured, writing to disk runs at about 180 MB/s, and the
file is written twice (as sent, and converted), so disk speed sets most of
the 12 s. Apps that still send the file inside `CreateModel` as `weights`
take about 5 GB for the same file, from reading the request into memory.

### Staying up when memory runs short

The console and every worker keep within a memory budget, so they slow
down and turn work away rather than run out and be killed. The budget is
`-memory-limit` (`$MIMIR_MEMORY_LIMIT`), such as `6GiB` or `75%`; without
it, `$GOMEMLIMIT`, else 85% of the container's memory limit, else 85% of
the machine's memory. It is also handed to Go's collector as its soft
limit, so garbage is collected harder before the budget is reached.

| memory in use | what happens |
| --- | --- |
| under 80% of the budget | everything runs |
| 80% (high) | half of the loaded models are let go; no new training run starts here, and a worker this high is given none |
| 92% (critical) | all loaded models but the last used are let go; training here stops and goes back in the queue, to resume from its checkpoint; no new model is loaded and no upload is read into memory (both answer "try again in a minute"), while loaded models keep answering |

Levels ease off 5 points under where they start. A run stopped for memory
waits a minute before it is tried again, and fails with an explanation
after three stops rather than starting over for ever. Training also runs
within three quarters of the budget: a batch too big for it is split into
smaller ones that add up to the same step.

You can watch it in `/metrics` (`mimir_memory_used_bytes`,
`mimir_memory_limit_bytes`, `mimir_memory_pressure`), in `/readyz`, which
stays 200 but adds a `memory:` line, in the logs, on each worker's card,
and by alert: owners hear when the console turns critical, at most once
an hour.

A panic in one request, prediction, model load, training run, webhook or
background loop fails only that piece of work: it is logged with its
stack, counted in `mimir_panics_recovered_total`, and the process carries
on. A worker that loses its link reconnects with backoff, and a run it was
training goes back in the queue and resumes from its checkpoint.

## Database connections

At every load above the main database had at most 11 connections open and
the log database 2, within the default pool of 16. No run needed more.

## What limits a console, in order

1. **Language models.** Generation speed in mimir's runtime (O(context)
   work per token in the cache update) and the CPU it takes from
   everything else. Fix belongs in mimir's `io/mim` scatter.
2. **CPU for predictions.** mimir's CPU helper goroutines spin while
   waiting for the next loop, 17–30% of the console's CPU under steady
   predictions.
PostgreSQL, the pool and the number of teams were not limits at these
loads.

## Running it again

```sh
MIMIR_SETUP_TOKEN=loadtest-setup-token-123 ./bin/mimir-server -data /tmp/d \
  -database-url 'postgres://...' -max-model-mb 2048
go run ./cmd/mimir-load -setup-token loadtest-setup-token-123 \
  -teams 8 -predict 2 -train 2 -lm model.gguf -chat 4 -duration 90s
```

Later runs against the same server reuse its teams, models and language
model. `-h` lists the knobs; set any count to 0 to leave that part out.
