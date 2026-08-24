# The container for the inventory sync service.
#
# Two stages. The first has the Go compiler and all the source; the second has
# exactly one binary file. The final container has no compiler, no shell, and no
# source — so there is nothing inside it that can be run except the service.

FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies are copied first, apart from the code. This layer is only rebuilt
# when go.mod changes — not every time one line of code is edited.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO is off so the binary needs no system libraries at all. That is what lets it
# run in the empty container below.
#
# -trimpath strips the build machine's paths from the binary; without it, a stack
# trace in production names the folder layout of the laptop that built it.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /service ./cmd/api


# ── The container that actually ships ──────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

# Root certificates, so TLS connections to Shopify and to the database can be
# verified. Without them every Admin API call fails with a confusing certificate
# error.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=build /service /service

EXPOSE 8080

# Runs as non-root, already so from the base image. This service needs no system
# privilege of any kind.
USER nonroot:nonroot

ENTRYPOINT ["/service"]
