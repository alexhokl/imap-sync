FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /imap-sync .

# Minimal runtime image — just the binary + CA certs for TLS.
FROM scratch
COPY --from=build /imap-sync /imap-sync
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

ENTRYPOINT ["/imap-sync"]
