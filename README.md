# mimir-server

A server for [mimir](https://github.com/gsoultan/mimir) models. Upload a
model's weights, then ask it questions from the web UI, over HTTP/JSON, or
over gRPC, all on one port.

This is phase 1: one binary, a SQLite database, a model store, prediction,
and the web UI's first screens. Users and roles, API keys, webhooks,
settings, training and workers come in later phases.

## Run it

You need Go 1.27 and [Bun](https://bun.sh).

```sh
make            # builds the web UI, then bin/mimir-server with it embedded
./bin/mimir-server -addr :8080 -data ./data
```

Open http://localhost:8080, choose **Add a model**, pick the model type and
upload a `.safetensors` file saved with `nn.Save` from mimir.

## How models load

A mimir model is Go code plus weights, so the server keeps a registry of
known model types in `internal/arch` (today: two handwritten-digit models,
`mnist-mlp` and `mnist-cnn`). An upload names its type, and the server
checks the weights fit that type before storing them. To serve your own
architecture, add it to the registry.

## Use it from your app

Every method works over Connect (HTTP/JSON), gRPC and gRPC-Web. The API is
in `proto/mimir/v1/model.proto`.

```sh
curl localhost:8080/mimir.v1.PredictService/Predict \
  -H 'Content-Type: application/json' \
  -d '{"modelId": "<id>", "input": {"values": [/* 784 numbers from 0 to 1 */]}}'

grpcurl -plaintext -d '{"model_id": "<id>", "input": {"values": [...]}}' \
  localhost:8080 mimir.v1.PredictService/Predict
```

gRPC runs over unencrypted HTTP/2 here; put a TLS proxy in front for
anything public. Health checks are at `grpc.health.v1.Health`.

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
| `internal/runtime` | Loads weights into mimir and runs them |
| `internal/store` | SQLite metadata and weight files |
| `internal/server` | HTTP wiring, health, web UI serving |
| `web` | React 19, Mantine 9, TanStack Router and Query, Connect, PWA |
