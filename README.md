# go-discord-irc — A Custom fork for W6YL

This is a heavily modified fork of the original project, intended for use on the https://irc.w6yl.org network and our Discord guild.
Use at your own risk.

Inexhaustive change list from upstream:

- The `IRC -> Discord` side of things work as you would expect it to: messages
  on IRC send to Discord as the bot user, as per usual.
- The `Discord -> IRC` side of things work using the IRCv3 `draft/relaymsg` capability to send PRIVMSG with a faked nick (in this case, the discord username)
- Reupload attachments to some pastebin
  - Because Discord now adds a timestamped signature to attachment URLs, and they expire. So raw `https://cdn.discordapp.com` links just rot in a few weeks, that's REALLY bad.
  - **NOTE** the HTTP interface is kind of hard coded to Rustypaste right now because that's what I use...
- Support forwarded messages and replies
- Removed config file reloading support to make the code much simpler (main reason, it was killing me flying all over the place), and binary slightly smaller (might as well)

> 🔔 You might know that https://github.com/42wim/matterbridge also supports `draft/relaymsg`.
> The unfortunate matter of fact is that when I found out about it, I've already spent lots of time rewriting this the upstream project to fit my needs, so it ended in like a sunken cost scenario.
>
> This bridge does have a few more additional features that matterbridge doesn't, namely:
> - Reupload attachments to some pastebin
> - Sending avatars from a configurable source like `https://github.com/${USERNAME}.png` when an identically named user doesn't exist on Discord (it supports checking for identical user, using the `UseLocalAvatar` option, but not the first part)
> - Forwarded message support

## Building

This branch relies on a tweaked version of go-ircevent that isn't published.

```
git clone --recursive https://github.com/rtk0c/go-discord-irc.git
cd ./go-discord-irc

# either build static binary:
go build
# or alternatively, if you want a docker image:
docker build .
```

This repo vendors a tweaked version of go-ircevents, using a git submodule, because the upstream version has broken support for IRCv3 cap negociation.

## Configuration

The binary takes three flags:

- `--config filename.yaml`: to pass along a configuration file containing things
  like passwords and channel options
- `--debug`: provide this flag to print extra debug info. Setting this flag to
  false (or not providing this flag) will take the value from the config file
  instead
- `--debug-presence`: similar to `--debug`, but for Discord user presence information

This bot needs permissions to manage webhooks as it creates webhooks on the go.

```
https://discordapp.com/oauth2/authorize?&client_id=<YOUR_CLIENT_ID_HERE>&scope=bot&permissions=0x20000000
```

## Publishing

Note to self on how to publish:

Follow https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry#authenticating-to-the-container-registry
to login into ghcr in docker.

```
docker loing
# This is just a temporary tag shorthand, while building
docker buildx build --platform=linux/amd64,linux/arm64 -t go-discord-irc .
docker tag go-discord-irc ghcr.io/rtk0c/go-discord-irc:latest
docker tag go-discord-irc ghcr.io/rtk0c/go-discord-irc:v1234
docker push --all-tags ghcr.io/rtk0c/go-discord-irc:latest
docker image rm go-discord-irc
```

(where `v1234` is the next version)


# Note on the design

Discord channel ID and guild ID are globally unique. The config could have only asked for channel ID and fetched the corresponding guild ID from the list of guilds the discord application ("bot") was invited to. But, I believe specifying both in config is more explicit and less "magic".

Note that there is no way check that you gave a valid guildID/channelID pair. If they're mismatched, the bridge will just panic.
