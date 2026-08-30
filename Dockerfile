FROM golang:1.26.6-bookworm AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/akritas ./cmd/akritas

FROM golang:1.26.6-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 --shell /usr/sbin/nologin akritas \
    && install -d -o akritas -g akritas /var/lib/akritas/audit /workspaces

COPY --from=build /out/akritas /usr/local/bin/akritas

USER akritas
WORKDIR /var/lib/akritas
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/akritas"]
CMD ["serve", "-address", "0.0.0.0:8090", "-audit-log", "/var/lib/akritas/audit/akritas.jsonl"]
