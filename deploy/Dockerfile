FROM node:26.13.1-alpine AS dashboard
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
COPY assets/brand /assets/brand
COPY docs/screenshots/fixture-overview-1440.png /docs/screenshots/
RUN npm run build

FROM golang:1.27.4-alpine AS domain
WORKDIR /src
RUN apk add --no-cache python3
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/bugsnitch-domain ./cmd/bugsnitch-domain
COPY internal ./internal
COPY scripts/go_notices.py ./scripts/go_notices.py
ENV CGO_ENABLED=0
RUN go build -trimpath -o /bugsnitch-domain ./cmd/bugsnitch-domain \
    && python3 scripts/go_notices.py ./cmd/bugsnitch-domain /licenses

FROM rust:1.100.0-alpine3.23 AS native
WORKDIR /src
RUN apk add --no-cache libpq-dev clang-dev pkgconf python3
COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY rust ./rust
COPY scripts/rust_notices.py ./scripts/rust_notices.py
COPY internal/storage/migrations ./internal/storage/migrations
COPY internal/audit/record.sql ./internal/audit/record.sql
COPY internal/incidents/sql ./internal/incidents/sql
COPY internal/transport/views ./internal/transport/views
# The server and coordinator use the runtime image's musl and libpq.
ENV RUSTFLAGS="-C target-feature=-crt-static"
RUN cargo build --locked --release --bins -p bugsnitch-server-migrate -p bugsnitch-provider \
    && python3 scripts/rust_notices.py bugsnitch-server-migrate /licenses \
    && python3 scripts/rust_notices.py bugsnitch-provider /licenses

FROM alpine:3.23
RUN apk add --no-cache ca-certificates libgcc libpq && adduser -D -u 10001 bugsnitch
COPY --from=domain /bugsnitch-domain /usr/local/bin/
COPY --from=native /src/target/release/bugsnitch-server /src/target/release/bugsnitch-server-migrate /src/target/release/bugsnitch-provider /usr/local/bin/
COPY --from=domain /licenses /usr/share/licenses/bugsnitch/
COPY --from=native /licenses /usr/share/licenses/bugsnitch/
COPY LICENSE /usr/share/licenses/bugsnitch/LICENSE
COPY --from=dashboard /web/dist /srv/dashboard
USER bugsnitch
# Exercise the dynamic loader before publishing the image. The Compose smoke
# starts the public listener, database and private domain worker together.
RUN bugsnitch-provider </dev/null \
    && (bugsnitch-server-migrate 2>&1 | grep -Fx "Set DATABASE_URL for the migrator")
ENV BUGSNITCH_LISTEN=0.0.0.0:8787 BUGSNITCH_WEB_DIR=/srv/dashboard
EXPOSE 8787
ENTRYPOINT ["bugsnitch-server"]
