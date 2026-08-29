ARG GOLANG_VERSION=1.27
FROM golang:$GOLANG_VERSION-alpine AS builder
WORKDIR /bot
COPY . .
RUN go build

FROM scratch
WORKDIR /bot
COPY --from=builder /bot/go-discord-irc .
# very nice thing due to using IRCv3 draft/relaymsg is the bridge can be entirely stateless
CMD ["./go-discord-irc", "--config", "config.yml"]
