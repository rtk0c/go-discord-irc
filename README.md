# Custom fork for W6YL

This is a heavily modified fork of the original project, intended for use on the https://irc.w6yl.org network and our Discord guild.
Use at your own risk.

# go-discord-irc

[![Go Report Card](https://goreportcard.com/badge/github.com/qaisjp/go-discord-irc)](https://goreportcard.com/report/github.com/qaisjp/go-discord-irc)
[![GoDoc](https://godoc.org/github.com/qaisjp/go-discord-irc?status.svg)](https://godoc.org/github.com/qaisjp/go-discord-irc)

[![Preview](https://i.imgur.com/YpCqzdn.gif)](https://i.imgur.com/YpCqzdn.webm)

**Is this being maintained?** Yes. But I want to merge all this functionality
into the much superior
[matterbridge by 42wim](https://github.com/42wim/matterbridge).

This is IRC to Discord bridge was originally built for
[@compsoc-edinburgh](http://github.com/compsoc-edinburgh) and
[ImaginaryNet](http://imaginarynet.uk/), but now it looks like more people are
using it!

- The `IRC -> Discord` side of things work as you would expect it to: messages
  on IRC send to Discord as the bot user, as per usual.
- The `Discord -> IRC` side of things work using the IRCv3 `draft/relaymsg` capability to send PRIVMSG with a faked nick (in this case, the discord username)

**Features**

(not a full list)

- Every Discord user, like `david`, will appear on IRC as a nick `david/d` (or whatever suffix your IRC server tells the bridge to use).
- Saying `<@david>` on IRC will mention `david` on Discord
- Replying to someone on Discord will prefix that someone's name, e.g. replying to Alex with "yes that's fine" will show up as `<david/d> Alex: yes, that's fine` on IRC.
- IRC users can send (custom!) emoji to Discord, just do `:somename:`. Discord emoji shows up like that on IRC.
- Reacting to a Discord message will send a CTCP ACTION (`/me`) on IRC.

## Configuration

The binary takes three flags:

- `--config filename.yaml`: to pass along a configuration file containing things
  like passwords and channel options
- `--debug`: provide this flag to print extra debug info. Setting this flag to
  false (or not providing this flag) will take the value from the config file
  instead
- `--debug-presence`: similar to `--debug`, but for Discord user presence information

TODO config file

The specified config file is continuously read from, and many changes will update on the bridge.
This means you can add or remove channels restarting the bot.

This bot needs permissions to manage webhooks as it creates webhooks on the go.

```
https://discordapp.com/oauth2/authorize?&client_id=<YOUR_CLIENT_ID_HERE>&scope=bot&permissions=0x20000000
```

## Docker

First edit `config.yml` file to your needs.
Then launch `docker build -t go-discord-irc .` in the repository root folder.
And then `docker run -d go-discord-irc` to run the bot in background.
