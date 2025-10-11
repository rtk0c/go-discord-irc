package bridge

import (
	"crypto/tls"
	"fmt"
	"strings"

	ircf "github.com/qaisjp/go-discord-irc/irc/format"
	irc "github.com/qaisjp/go-ircevent"
	log "github.com/sirupsen/logrus"
)

type ircListener struct {
	*irc.Connection
	bridge *Bridge

	listenerCallbackIDs map[string]int
}

func newIRCListener(dib *Bridge) *ircListener {
	irccon := irc.IRC(dib.Config.IRCBotNick, "discord")
	listener := &ircListener{irccon, dib, make(map[string]int)}

	if !dib.Config.NoTLS {
		irccon.UseTLS = true
		irccon.TLSConfig = &tls.Config{
			InsecureSkipVerify: dib.Config.InsecureSkipVerify,
		}
	}

	// On kick, rejoin the channel
	irccon.AddCallback("KICK", func(e *irc.Event) {
		if e.Arguments[1] == irccon.GetNick() {
			irccon.Join(e.Arguments[0])
		}
	})

	irccon.Password = dib.Config.IRCServerPass
	irccon.UseSASL = dib.Config.SaslLogin != ""
	irccon.SASLLogin = dib.Config.SaslLogin
	irccon.SASLPassword = dib.Config.SaslPassword

	listener.SetDebugMode(dib.Config.Debug)

	// Request relaymsg caps
	irccon.RequestCaps["draft/relaymsg"] = true

	// Welcome event
	irccon.AddCallback("001", listener.OnWelcome)

	// Called when received channel names... essentially OnJoinChannel
	irccon.AddCallback("366", listener.OnJoinChannel)
	irccon.AddCallback("PRIVMSG", listener.OnPrivateMessage)
	irccon.AddCallback("NOTICE", listener.OnPrivateMessage)
	irccon.AddCallback("CTCP_ACTION", listener.OnPrivateMessage)

	irccon.AddCallback("900", func(e *irc.Event) {
		// Try to rejoni channels after authenticated with NickServ
		listener.JoinChannels()
	})

	// we are assuming this will be posible to run independent of any
	// future NICK callbacks added, otherwise do it like the STQUIT callback
	listener.AddCallback("NICK", listener.nickTrackNick)

	// Nick tracker for nick tracking
	irccon.SetupNickTrack()
	// we're either going to track quits, or track and relay said, so swap out the callback
	// based on which is in effect.
	if dib.Config.ShowJoinQuit {
		listener.listenerCallbackIDs["STNICK"] = listener.AddCallback("STNICK", listener.OnNickRelayToDiscord)

		// KICK is not state tracked!
		callbacks := []string{"STJOIN", "STPART", "STQUIT", "KICK"}
		for _, cb := range callbacks {
			id := listener.AddCallback(cb, listener.OnJoinQuitCallback)
			listener.listenerCallbackIDs[cb] = id
		}
	} else {
		id := listener.AddCallback("STQUIT", listener.nickTrackPuppetQuit)
		listener.listenerCallbackIDs["STQUIT"] = id
	}

	return listener
}

func (i *ircListener) nickTrackNick(event *irc.Event) {
	// TODO(rtk0c): delete func?
}

func (i *ircListener) OnNickRelayToDiscord(event *irc.Event) {
	// ignored hostmasks, or we're a puppet? no relay
	if i.isIgnoredHostmask(event.Source) ||
		i.isPuppetNick(event.Nick) ||
		i.isPuppetNick(event.Message()) {
		return
	}

	oldNick := event.Nick
	newNick := event.Message()

	msg := IRCMessage{
		Username: "",
		Message:  fmt.Sprintf("_%s changed their nick to %s_", oldNick, newNick),
	}

	for channel := range i.bridge.Config.ChannelMappings {
		if channelObj, ok := i.Connection.GetChannel(channel); ok {
			if _, ok := channelObj.GetUser(newNick); ok {
				msg.IRCChannel = channel
				i.bridge.discordMessagesChan <- msg
			}
		}
	}
}

func (i *ircListener) nickTrackPuppetQuit(e *irc.Event) {
	// TODO(rtk0c): delete func?
}

func (i *ircListener) OnJoinQuitCallback(event *irc.Event) {
	// This checks if the source of the event was from a puppet.
	if (event.Code == "KICK" && i.isPuppetNick(event.Arguments[1])) || i.isPuppetNick(event.Nick) {
		// since we replace the STQUIT callback we have to manage our puppet nicks when
		// this call back is active!
		if event.Code == "STQUIT" {
			i.nickTrackPuppetQuit(event)
		}
		return
	}

	// Ignored hostmasks
	if i.isIgnoredHostmask(event.Source) {
		return
	}

	who := event.Nick
	message := event.Nick
	id := " (" + event.User + "@" + event.Host + ") "

	switch event.Code {
	case "STJOIN":
		message += " joined" + id
	case "STPART":
		message += " left" + id
		if len(event.Arguments) > 1 {
			message += ": " + event.Arguments[1]
		}
	case "STQUIT":
		message += " quit" + id

		reason := event.Nick
		if len(event.Arguments) == 1 {
			reason = event.Arguments[0]
		}
		message += "Quit: " + reason
	case "KICK":
		who = event.Arguments[1]
		message = event.Arguments[1] + " was kicked by " + event.Nick + ": " + event.Arguments[2]
	}

	msg := IRCMessage{
		// IRCChannel: set on the fly
		Username: "",
		Message:  message,
	}

	if event.Code == "STQUIT" {
		// Notify channels that the user is in
		for channel := range i.bridge.Config.ChannelMappings {
			channelObj, ok := i.Connection.GetChannel(channel)
			if !ok {
				log.WithField("channel", channel).WithField("who", who).Warnln("Trying to process QUIT. Channel not found in irc listener cache.")
				continue
			}
			if _, ok := channelObj.GetUser(who); !ok {
				continue
			}
			msg.IRCChannel = channel
			i.bridge.discordMessagesChan <- msg
		}
	} else {
		msg.IRCChannel = event.Arguments[0]
		i.bridge.discordMessagesChan <- msg
	}
}

// FIXME: the user might not be on any channel that we're in and that would
// lead to incorrect assumptions the user doesn't exist!
// Good way to check is to utilize ISON
func (i *ircListener) DoesUserExist(user string) bool {
	ret := false
	i.IterChannels(func(name string, ch *irc.Channel) {
		if !ret {
			_, ret = ch.GetUser(user)
		}
	})
	return ret
}

func (i *ircListener) SetDebugMode(debug bool) {
	i.VerboseCallbackHandler = debug
	i.Debug = debug
}

func (i *ircListener) OnWelcome(e *irc.Event) {
	// Execute prejoin commands
	for _, com := range i.bridge.Config.IRCListenerPrejoinCommands {
		i.SendRaw(strings.ReplaceAll(com, "${NICK}", i.GetNick()))
	}

	// Join all channels
	i.JoinChannels()
}

func (i *ircListener) JoinChannels() {
	var channels, keyedChannels, keys []string

	config := i.bridge.Config

	for channel := range config.ChannelMappings {
		key, isKeyed := config.ircChannelKeys[channel]

		if isKeyed {
			keyedChannels = append(keyedChannels, channel)
			keys = append(keys, key)
		} else {
			channels = append(channels, channel)
		}
	}

	// Just append normal channels to the end of keyed channelsG
	keyedChannels = append(keyedChannels, channels...)

	joinCommand := "JOIN " + strings.Join(keyedChannels, ",") + " " + strings.Join(keys, ",")

	i.SendRaw(joinCommand)
}

func (i *ircListener) OnJoinChannel(e *irc.Event) {
	log.Infof("Listener has joined IRC channel %s.", e.Arguments[1])
}

func (i *ircListener) isPuppetNick(nick string) bool {
	if i.GetNick() == nick {
		return true
	}
	// TODO check for draft/relaymsg reserved char format
	return false
}

func (i *ircListener) OnPrivateMessage(e *irc.Event) {
	// Ignore private messages
	if string(e.Arguments[0][0]) != "#" {
		// If you decide to extend this to respond to PMs, make sure
		// you do not respond to NOTICEs, see issue #50.
		return
	}

	if strings.HasSuffix(e.Nick, i.bridge.IRCPuppeteer.usernameDecoration) {
		return
	}
	// TODO fix tags parsing
	// if botnick, ok := e.Tags["draft/relaymsg"]; ok && botnick == i.GetNick() {
	// 	return
	// }

	if i.isPuppetNick(e.Nick) || // ignore msg's from our puppets
		i.isIgnoredHostmask(e.Source) || //ignored hostmasks
		i.isFilteredIRCMessage(e.Message()) { // filtered
		return
	}

	// TODO(rtk0c): transform IRC nick to discord username
	replacements := []string{}
	msg := strings.NewReplacer(
		replacements...,
	).Replace(e.Message())

	if e.Code == "CTCP_ACTION" {
		msg = "_" + msg + "_"
	}

	msg = ircf.BlocksToMarkdown(ircf.Parse(msg))

	go func(e *irc.Event) {
		i.bridge.discordMessagesChan <- IRCMessage{
			IRCChannel: e.Arguments[0],
			Username:   e.Nick,
			Message:    msg,
		}
	}(e)
}

func (i *ircListener) isIgnoredHostmask(mask string) bool {
	for _, ban := range i.bridge.Config.IRCIgnores {
		if ban.Match(mask) {
			return true
		}
	}
	return false
}

func (i *ircListener) isFilteredIRCMessage(txt string) bool {
	for _, ban := range i.bridge.Config.IRCFilteredMessages {
		if ban.Match(txt) {
			return true
		}
	}
	return false
}
