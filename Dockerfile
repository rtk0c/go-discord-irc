ARG GOLANG_VERSION=1.27
FROM --platform=$BUILDPLATFORM golang:$GOLANG_VERSION-alpine AS builder
RUN --mount=type=cache,target=/go/pkg/mod
WORKDIR /bot
COPY . .
ARG TARGETOS TARGETARCH
# Not doing `-ldflags="-s -w"` because it's only ~5MB (stripped 8.8MB, full 13MB) and it helps immensely if the bridge crashes for some reason
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o ./go-discord-irc .

FROM scratch
WORKDIR /bot
COPY --from=builder /bot/go-discord-irc .
# very nice thing due to using IRCv3 draft/relaymsg is the bridge can be entirely stateless
CMD ["./go-discord-irc", "--config", "config.yml"]
