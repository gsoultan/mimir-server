# syntax=docker/dockerfile:1

# mimir-server as a static binary on distroless, running as a non-root
# user. Build with:
#
#   docker build --build-arg VERSION=$(git describe --tags --always) -t mimir-server .
#
# and run with the database URL and a volume for the data folder:
#
#   docker run -p 8080:8080 -v mimir-data:/data \
#     -e MIMIR_DATABASE_URL=postgres://... mimir-server
#
# The image has no GPU support: mimir loads the CUDA and ROCm libraries
# through the C library's dynamic loader, which a static binary does not
# have, so the nocuda and norocm tags leave those backends out. Nor does it
# have pg_dump, so Settings > Database > Download a backup answers 501 here;
# back up with pg_dump from elsewhere.

# The web UI, embedded into the binary from web/dist.
FROM oven/bun:1.4.2 AS web
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -tags nocuda,norocm \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/mimir-server ./cmd/mimir-server \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mimir-server /usr/local/bin/mimir-server
# The data folder: model weights, datasets, checkpoints, the TLS
# certificate made by -tls-auto and the secret key (unless MIMIR_SECRET_KEY
# is set). Owned by the image's nonroot user (65532).
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mimir-server"]
CMD ["-addr", ":8080", "-data", "/data"]
