package bridge

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mozillazg/go-unidecode"
	"github.com/pkg/errors"

	ircnick "github.com/qaisjp/go-discord-irc/irc/nick"
	log "github.com/sirupsen/logrus"
)

// DevMode is a hack
var DevMode = false

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

func (m *IRCPuppeteer) ircIgnoredDiscord(user string) bool {
	_, ret := m.bridge.Config.DiscordIgnores[user]
	return ret
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

func (m *IRCPuppeteer) generateNickname(discord DiscordUser) string {
	nick := sanitiseNickname(discord.Nick)
	suffix := m.bridge.Config.Suffix
	newNick := nick + suffix

	useFallback := len(newNick) > m.bridge.Config.MaxNickLength || m.bridge.ircListener.DoesUserExist(newNick)
	// log.WithFields(log.Fields{
	// 	"length":      len(newNick) > ircnick.MAXLENGTH,
	// 	"useFallback": useFallback,
	// }).Infoln("nickgen: fallback?")

	if !useFallback {
		guild, err := m.bridge.discord.Session.State.Guild(m.bridge.Config.GuildID)
		if err != nil {
			// log.Fatalln("nickgen: guild not found when generating nickname")
			return ""
		}

		for _, member := range guild.Members {
			if member.User.ID == discord.ID {
				continue
			}

			name := member.Nick
			if member.Nick == "" {
				name = member.User.Username
			}

			if name == "" {
				log.WithField("member", member).Errorln("blank username encountered")
				continue
			}

			if strings.EqualFold(sanitiseNickname(name), nick) {
				// log.WithField("member", member).Infoln("nickgen: using fallback because of discord")
				useFallback = true
				break
			}
		}
	}

	if useFallback {
		discriminator := discord.Discriminator
		username := sanitiseNickname(discord.Username)
		suffix = m.bridge.Config.Separator + discriminator + suffix

		// Maximum length of a username but without the suffix
		length := ircnick.MAXLENGTH - len(suffix)
		if length >= len(username) {
			length = len(username)
			// log.Infoln("nickgen: maximum length limit not reached")
		}

		newNick = username[:length] + suffix
		// log.WithFields(log.Fields{
		// 	"nick":     discord.Nick,
		// 	"username": discord.Username,
		// 	"newNick":  newNick,
		// }).Infoln("nickgen: resultant nick after falling back")
		return newNick
	}

	// log.WithFields(log.Fields{
	// 	"nick":     discord.Nick,
	// 	"username": discord.Username,
	// 	"newNick":  newNick,
	// }).Infoln("nickgen: resultant nick WITHOUT falling back")

	return newNick
}

// SendMessage sends a broken down Discord Message to a particular IRC channel.
func (m *IRCPuppeteer) SendMessage(channel string, msg *DiscordMessage) {
	if m.ircIgnoredDiscord(msg.Author.ID) {
		return
	}

	content := msg.Content

	channel = strings.Split(channel, " ")[0]

	useRelayMsg := m.IsUsingRelayMsg()

	length := len(msg.Author.Username)
	for _, line := range strings.Split(content, "\n") {
		// if strings.HasPrefix(line, "/me ") && len(line) > 4 {
		// 	ircMessage.IsAction = true
		// 	ircMessage.Message = line[4:]
		// }

		if useRelayMsg {
			username := sanitiseNickname(msg.Author.Username)

			var fmtstr string
			if msg.IsAction {
				fmtstr = "RELAYMSG %s %s :\x01ACTION %s\x01"
			} else {
				fmtstr = "RELAYMSG %s %s :%s"
			}
			m.bridge.ircListener.SendRawf(fmtstr, channel, username, line)
		} else {
			line = fmt.Sprintf(
				"<%s#%s> %s",
				// TODO(rtk0c) what's the point of using U+200B ZERO WIDTH SPACE?
				msg.Author.Username[:1]+"\u200B"+msg.Author.Username[1:length],
				// TODO(rtk0c) discord no longer uses this
				msg.Author.Discriminator,
				line,
			)
			m.bridge.ircListener.Privmsg(channel, line)
		}
	}
}

// RequestChannels finds all the Discord channels this user belongs to,
// and then find pairings in the global pairings list
// Currently just returns all participating IRC channels
// TODO (?)
func (m *IRCPuppeteer) RequestChannels(userID string) []Mapping {
	return m.bridge.mappings
}

func (m *IRCPuppeteer) isIgnoredHostmask(mask string) bool {
	for _, ban := range m.bridge.Config.IRCIgnores {
		if ban.Match(mask) {
			return true
		}
	}
	return false
}

func (m *IRCPuppeteer) isFilteredIRCMessage(txt string) bool {
	for _, ban := range m.bridge.Config.IRCFilteredMessages {
		if ban.Match(txt) {
			return true
		}
	}
	return false
}

func (m *IRCPuppeteer) isFilteredDiscordMessage(txt string) bool {
	for _, ban := range m.bridge.Config.DiscordFilteredMessages {
		if ban.Match(txt) {
			return true
		}
	}
	return false
}

func (m *IRCPuppeteer) generateUsername(discordUser DiscordUser) string {
	if len(m.bridge.Config.PuppetUsername) > 0 {
		return m.bridge.Config.PuppetUsername
	}
	return sanitiseNickname(discordUser.Username)
}
