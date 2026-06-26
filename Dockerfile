# Multi-stage build → a few-MB static image (NFR-D1, NFR-D3).

# ---- build stage ----
FROM golang:1.26 AS build
WORKDIR /src

# Cache dependencies first so source edits don't re-download modules.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
# CGO_ENABLED=0 → a static binary that runs in a scratch image (NFR-D1).
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/chat ./cmd/chat

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/chat /chat
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/chat"]
