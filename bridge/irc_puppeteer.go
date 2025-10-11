package bridge

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/mozillazg/go-unidecode"
	"github.com/pkg/errors"

	ircnick "github.com/qaisjp/go-discord-irc/irc/nick"
)

// IRCPuppeteer should only be used from one thread.
type IRCPuppeteer struct {
	bridge *Bridge

	// String to append to Discord username when becoming a puppet.
	usernameDecoration string
}

func newIRCPuppeteer(bridge *Bridge) (*IRCPuppeteer, error) {
	m := &IRCPuppeteer{
		bridge: bridge,
	}
	return m, nil
}

func firstRune(s string) rune {
	for _, c := range s {
		return c
	}
	return rune(0)
}

// Setup puppeteer options, drawing the negotiated IRC capabilities.
func (m *IRCPuppeteer) setupCaps() {
	// draft/relaymsg wording:
	// > If this capability has a value, the given characters are 'nickname separators'.
	// > These characters aren't allowed in normal nicknames, and if given one MUST be
	// > present in spoofed nicknames. For example, with `draft/relaymsg=/` the spoofed
	// > nickname MUST include the character `"/"`.
	i := m.bridge.ircListener
	for _, capName := range i.AcknowledgedCaps {
		if capName == "draft/relaymsg" {
			reservedChars := i.AvailableCaps[capName]

			separator := firstRune(reservedChars)
			if separator == rune(0) {
				// If server didn't provide a reserved chars, add a courtesy [d] suffix
				m.usernameDecoration = "[d]"
			} else {
				// If server providede.g. / and decorate as /d
				m.usernameDecoration = string(separator) + "d"
			}
			break
		}
	}
}

func (m *IRCPuppeteer) IsUsingRelayMsg() bool {
	return m.usernameDecoration != ""
}

// Close closes all of an IRCPuppeteer's connections.
func (m *IRCPuppeteer) Close() {
}

// Converts a nickname to a sanitised form.
// Does not check IRC or Discord existence, so don't use this method
// unless you're also checking IRC and Discord.
func sanitiseNickname(nick string) string {
	if nick == "" {
		fmt.Println(errors.WithStack(errors.New("trying to sanitise an empty nick")))
		return "_"
	}

	// Unidecode the nickname — we make sure it's not empty to prevent "🔴🔴" becoming ""
	if newnick := unidecode.Unidecode(nick); newnick != "" {
		nick = newnick
	}

	// https://github.com/lp0/charybdis/blob/9ced2a7932dddd069636fe6fe8e9faa6db904703/ircd/client.c#L854-L884
	if nick[0] == '-' {
		nick = "_" + nick
	}
	if ircnick.IsDigit(nick[0]) {
		nick = "_" + nick
	}

	newNick := []byte(nick)

	// Replace bad characters with underscores
	for i, c := range []byte(nick) {
		if !ircnick.IsNickChar(c) || ircnick.IsFakeNickChar(c) {
			newNick[i] = ' '
		}
	}

	// Now every invalid character has been replaced with a space (just some invalid character)
	// Lets replace each sequence of invalid characters with a single underscore
	newNick = regexp.MustCompile(` +`).ReplaceAllLiteral(newNick, []byte{'_'})

	return string(newNick)
}

func (m *IRCPuppeteer) generateNickname(discord *discordgo.User) string {
	orig := sanitiseNickname(discord.Username)
	new := orig + m.usernameDecoration

	return new
}

// SendMessage sends a broken down Discord Message to a particular IRC channel.
func (m *IRCPuppeteer) SendMessage(channel string, msg *DiscordMessage) {
	content := msg.Content
	authorNick := m.generateNickname(msg.Author)

	channel = strings.Split(channel, " ")[0]

	useRelayMsg := m.IsUsingRelayMsg()

	for _, line := range strings.Split(content, "\n") {
		if useRelayMsg {
			var fmtstr string
			if msg.IsAction {
				fmtstr = "RELAYMSG %s %s :\x01ACTION %s\x01"
			} else {
				fmtstr = "RELAYMSG %s %s :%s"
			}
			m.bridge.ircListener.SendRawf(fmtstr, channel, authorNick, line)
		} else {
			line = fmt.Sprintf("<%s> %s", authorNick, line)
			m.bridge.ircListener.Privmsg(channel, line)
		}
	}
}
