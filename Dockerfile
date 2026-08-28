# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies change far less often than the code, so they get their own
# layer and a rebuild after an ordinary commit skips the download entirely.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off produces a static binary, which is what the distroless runtime needs.
# Migrations are embedded into it, so this one file is the whole deployable —
# there is no separate migration step to run before the service starts.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# No shell, no package manager, nothing to exec into. It does carry CA
# certificates, which the managed Postgres connection needs for TLS.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server

# Hosts inject their own PORT and the server reads it; this is only the default
# for running the image directly.
ENV PORT=8080
EXPOSE 8080

USER nonroot:nonroot
ENTRYPOINT ["/server"]
