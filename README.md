# go-discord-irc — A Custom fork for W6YL

This is a heavily modified fork of the original project, intended for use on the https://irc.w6yl.org network and our Discord guild.
Use at your own risk.

Inexhaustive change list from upstream:

- The `IRC -> Discord` side of things work as you would expect it to: messages
  on IRC send to Discord as the bot user, as per usual.
- The `Discord -> IRC` side of things work using the IRCv3 `draft/relaymsg` capability to send PRIVMSG with a faked nick (in this case, the discord username)
- Reupload attachments to some pastebin
  - Because Discord now adds a timestamped signature to attachment URLs, and they expire. So raw `https://cdn.discordapp.com` links just rot in a few weeks, that's REALLY bad.
- Support forwarded messages and replies
- Removed config file reloading support to make the code much simpler (main reason, it was killing me flying all over the place), and binary slightly smaller (might as well)

## Building

This branch relies on a tweaked version of go-ircevent that isn't published.

1. Clone this repo, this specific branch to ./go-discord-irc
2. Clone https://github.com/rtk0c/go-ircevent/tree/ircv3-fixes (that specific branch) to ./go-ircevent
3. ```
   cd ./go-discord-irc
   go build
   ```

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
