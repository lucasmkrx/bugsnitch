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

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 bugsnitch
COPY --from=build /server /usr/local/bin/bugsnitch-server
COPY --from=dashboard /web/dist /srv/dashboard
USER bugsnitch
ENV BUGSNITCH_LISTEN=0.0.0.0:8787 BUGSNITCH_WEB_DIR=/srv/dashboard
EXPOSE 8787
ENTRYPOINT ["bugsnitch-server"]
