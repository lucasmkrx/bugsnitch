FROM node:26.13.1-alpine AS dashboard
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
COPY assets/brand /assets/brand
RUN npm run build

FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /server ./cmd/bugsnitch-server

FROM rust:1.100.0-alpine3.23 AS migrations
WORKDIR /src
RUN apk add --no-cache libpq-dev clang-dev pkgconf python3
COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY rust ./rust
COPY scripts/rust_notices.py /src/scripts/rust_notices.py
COPY internal/storage/migrations ./internal/storage/migrations
# Link the migrator against the runtime image's musl and libpq.
RUN RUSTFLAGS="-C target-feature=-crt-static" cargo build --locked --release --manifest-path rust/server-migrate/Cargo.toml

RUN python3 scripts/rust_notices.py bugsnitch-server-migrate /licenses

FROM alpine:3.23
RUN apk add --no-cache ca-certificates libpq && adduser -D -u 10001 bugsnitch
COPY --from=build /server /usr/local/bin/bugsnitch-server
COPY --from=migrations /src/target/release/bugsnitch-server-migrate /usr/local/bin/bugsnitch-server-migrate
COPY --from=migrations /licenses /usr/share/licenses/bugsnitch-server-migrate
COPY --from=dashboard /web/dist /srv/dashboard
USER bugsnitch
ENV BUGSNITCH_LISTEN=0.0.0.0:8787 BUGSNITCH_WEB_DIR=/srv/dashboard
EXPOSE 8787
ENTRYPOINT ["bugsnitch-server"]
