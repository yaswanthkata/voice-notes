# Build a static binary, then ship it on a minimal, non-root base image.
#
# The build stage runs on the build machine's architecture and Go
# cross-compiles for the target, so building linux/amd64 on an ARM Mac is
# fast (no emulation): docker build --platform linux/amd64 .
# The defaults only apply to builders without BuildKit (which sets these itself).
ARG BUILDPLATFORM=linux/amd64
ARG TARGETOS=linux
ARG TARGETARCH=amd64
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/voice-notes ./cmd/server

# distroless/static has CA certificates (needed for HTTPS to Gemini and the
# CRMs) but no shell or package manager, which keeps the attack surface small.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/voice-notes /voice-notes
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/voice-notes"]
