package bridge

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/gobwas/glob"
	"github.com/pkg/errors"
	ircnick "github.com/qaisjp/go-discord-irc/irc/nick"
	log "github.com/sirupsen/logrus"
)

type JsonSet map[string]struct{}

func (s *JsonSet) UnmarshalJSON(data []byte) error {
	var list []string
	err := json.Unmarshal(data, &list)
	if err != nil {
		return err
	}

	*s = make(map[string]struct{}, len(list))
	for _, v := range list {
		(*s)[v] = struct{}{}
	}
	return nil
}

// [glob.Glob] is an interface, so we have to emulate its exposed surface area here
type JsonGlob struct {
	g glob.Glob
}

func (g JsonGlob) Match(s string) bool {
	return g.g.Match(s)
}

func (s JsonGlob) UnmarshalJSON(data []byte) error {
	var text string
	err := json.Unmarshal(data, &text)
	if err != nil {
		return err
	}

	filter, err := glob.Compile(text)
	if err != nil {
		return err
	}

	s.g = filter
	return nil
}

// Config to be passed to New
type Config struct {
	AvatarURL       string
	DiscordBotToken string
	GuildID         string // Guild to/from which to bridge messages

	// Map from Discord to IRC
	ChannelMappings map[string]string
	// Parsed on load from `ChannelMappings`
	ircChannelKeys map[string]string // From "#test" to "password"

	IRCServer     string // Server address to use, example `irc.freenode.net:7000`.
	IRCServerPass string // Optional password for connecting to the IRC server
	IRCBotNick    string // i.e, "DiscordBot", required to listen for messages in all cases

	// If not "", perform SASL authentication during connection.
	// Otherwise, if needed, login needs to be configured manually through `IRCListenerPrejoinCommands`
	SaslLogin    string
	SaslPassword string

	IRCPuppetPrejoinCommands   []string // Commands for each connection to send before joining channels
	IRCListenerPrejoinCommands []string

	IRCIgnores     []JsonGlob // Ignore users with matching hostname
	DiscordIgnores JsonSet    // Discord user IDs to not bridge
	DiscordAllowed JsonSet    // Discord user IDs to only bridge

	IRCFilteredMessages     []JsonGlob // Ignore lines containing matched text from IRC
	DiscordFilteredMessages []JsonGlob // Ignore lines containing matched text from Discord

	// NoTLS constrols whether to use TLS at all when connecting to the IRC server
	NoTLS bool

	// InsecureSkipVerify controls whether a client verifies the
	// server's certificate chain and host name.
	// If InsecureSkipVerify is true, TLS accepts any certificate
	// presented by the server and any host name in that certificate.
	// In this mode, TLS is susceptible to man-in-the-middle attacks.
	// This should be used only for testing.
	InsecureSkipVerify bool

	// ShowJoinQuit determines whether or not to show JOIN, QUIT, KICK messages on Discord
	ShowJoinQuit bool

	// Maximum Nicklength for irc server
	// TODO respect this value
	MaxNickLength int

	Debug         bool
	DebugPresence bool
}

func MakeDefaultConfig() Config {
	return Config{
		IRCPuppetPrejoinCommands: []string{"MODE ${NICK} +D"},
		AvatarURL:                "https://robohash.org/${USERNAME}.png?set=set4",
		IRCBotNick:               "~d",
		ShowJoinQuit:             false,
		MaxNickLength:            ircnick.MAXLENGTH,
	}
}

func LoadConfigFile(into *Config, r io.Reader) error {
	err := json.NewDecoder(r).Decode(&into)
	if err != nil {
		return err
	}

	if len(into.ChannelMappings) == 0 {
		log.Warnln("Channel mappings are missing!")
	}

	return nil
}

// Compute auxiliary information (all private fields) from the primary config (public fields)
func resolveConfigAux(config *Config) {
	mappings := config.ChannelMappings

	config.ircChannelKeys = make(map[string]string, len(mappings))

	for irc, discord := range mappings {
		ircParts := strings.Split(irc, " ")
		ircChannel := ircParts[0]
		if parts := len(ircParts); parts != 1 && parts > 2 {
			log.Errorf("IRC channel irc %+v (to discord %+v) is invalid. Expected 0 or 1 spaces in the string. Ignoring.", irc, discord)
			continue
		} else if parts == 2 {
			config.ircChannelKeys[ircChannel] = ircParts[1]
		}
	}
}

// A Bridge represents a bridging between an IRC server and channels in a Discord server
type Bridge struct {
	Config *Config

	discord      *discordBot
	ircListener  *ircListener
	IRCPuppeteer *IRCPuppeteer

	done chan bool

	discordMessagesChan      chan IRCMessage
	discordMessageEventsChan chan *DiscordMessage
	updateUserChan           chan DiscordUser
	removeUserChan           chan string // user id

	emoji map[string]*discordgo.Emoji
}

// Close the Bridge
func (b *Bridge) Close() {
	b.done <- true
	<-b.done
}

// New Bridge
func New(conf *Config) (*Bridge, error) {
	resolveConfigAux(conf)

	dib := &Bridge{
		Config: conf,
		done:   make(chan bool),

		discordMessagesChan:      make(chan IRCMessage),
		discordMessageEventsChan: make(chan *DiscordMessage),
		updateUserChan:           make(chan DiscordUser),
		removeUserChan:           make(chan string),

		emoji: make(map[string]*discordgo.Emoji),
	}

	var err error

	dib.discord, err = newDiscord(dib, conf.DiscordBotToken, conf.GuildID)
	if err != nil {
		return nil, errors.Wrap(err, "Could not create discord bot")
	}

	dib.ircListener = newIRCListener(dib)
	if dib.IRCPuppeteer, err = newIRCPuppeteer(dib); err != nil {
		return nil, fmt.Errorf("failed to create IRCPuppeteer: %w", err)
	}

	go dib.loop()

	return dib, nil
}

// Open all the connections required to run the bridge
func (b *Bridge) Open() (err error) {

	// Open a websocket connection to Discord and begin listening.
	err = b.discord.Open()
	if err != nil {
		return errors.Wrap(err, "can't open discord")
	}

	err = b.ircListener.Connect(b.Config.IRCServer)
	if err != nil {
		return errors.Wrap(err, "can't open irc connection")
	}

	b.IRCPuppeteer.setupCaps()

	// run listener loop
	go b.ircListener.Loop()

	return
}

// GetMappingByIRC returns a Mapping for a given IRC channel.
// Returns nil if a Mapping does not exist.
func (b *Bridge) GetMappingByIRC(channel string) (string, bool) {
	for ircChannel, discordChannel := range b.Config.ChannelMappings {
		if strings.EqualFold(ircChannel, channel) {
			return discordChannel, true
		}
	}
	return "", false
}

// GetMappingByDiscord returns a Mapping for a given Discord channel.
// Returns nil if a Mapping does not exist.
func (b *Bridge) GetMappingByDiscord(channel string) (string, bool) {
	for ircChannel, discordChannel := range b.Config.ChannelMappings {
		if discordChannel == channel {
			return ircChannel, true
		}
	}
	return "", false
}

var emojiRegex = regexp.MustCompile("(:[a-zA-Z_-]+:)")

func (b *Bridge) loop() {
	for {
		select {

		// Messages from IRC to Discord
		case msg := <-b.discordMessagesChan:
			mappedDiscordChannel, ok := b.GetMappingByIRC(msg.IRCChannel)

			if !ok {
				log.Warnln("Ignoring message sent from an unhandled IRC channel.")
				continue
			}

			var avatar string
			username := msg.Username

			// System messages have no username
			if username != "" {
				avatar = b.discord.GetAvatar(b.Config.GuildID, msg.Username)
				if avatar == "" {
					// If we don't have a Discord avatar, generate an adorable avatar
					avatar = strings.ReplaceAll(b.Config.AvatarURL, "${USERNAME}", msg.Username)
				}

				if len(username) == 1 {
					// Append usernames with 1 character
					// This is because Discord doesn't accept single character usernames
					username += `.` // <- zero width space in here, ayylmao
				}
			}

			content := msg.Message

			// If the message has leading or trailing spaces, or if the message consists
			// entirely of whitespace, we want Discord to display them as intended,
			// rather than ignoring it. We surround the content with zero-width spaces
			// to achieve this. For example, 3 space characters sent from IRC should
			// render on Discord as 3 space characters too.
			if content == "" || strings.TrimSpace(content) != content {
				content = "\u200B" + content + "\u200B"
			}

			// Convert any emoji ye?
			content = emojiRegex.ReplaceAllStringFunc(content, func(emoji string) string {
				e, ok := b.emoji[strings.ToLower(emoji[1:len(emoji)-1])]
				if !ok {
					return emoji
				}

				emoji = ":" + e.Name + ":" + e.ID
				if e.Animated {
					emoji = "a" + emoji
				}

				return "<" + emoji + ">"
			})

			if username == "" {
				// System messages come straight from the bot
				if _, err := b.discord.Session.ChannelMessageSend(mappedDiscordChannel, content); err != nil {
					log.WithError(err).WithFields(log.Fields{
						"msg.channel":  mappedDiscordChannel,
						"msg.username": username,
						"msg.content":  content,
					}).Errorln("could not transmit SYSTEM message to discord")
				}
			} else {
				go func() {
					_, err := b.discord.transmitter.Send(
						mappedDiscordChannel,
						&discordgo.WebhookParams{
							Username:  username,
							AvatarURL: avatar,
							Content:   content,
							AllowedMentions: &discordgo.MessageAllowedMentions{
								// Allow user and role mentions, but not everyone or here mentions
								Parse: []discordgo.AllowedMentionType{
									discordgo.AllowedMentionTypeRoles,
									discordgo.AllowedMentionTypeUsers,
								},
							},
						},
					)

					if err != nil {
						log.WithFields(log.Fields{
							"error":        err,
							"msg.channel":  mappedDiscordChannel,
							"msg.username": username,
							"msg.avatar":   avatar,
							"msg.content":  content,
						}).Errorln("could not transmit message to discord")
					}
				}()
			}

		// Messages from Discord to IRC
		case msg := <-b.discordMessageEventsChan:
			mappedIRCChannel, ok := b.GetMappingByDiscord(msg.ChannelID)
			// Do not do anything if we do not have a mapping for the PUBLIC channel
			if !ok {
				continue
			}

			_, authorIgnored := b.Config.DiscordIgnores[msg.Author.ID]
			if authorIgnored {
				continue
			}

			b.IRCPuppeteer.SendMessage(mappedIRCChannel, msg)

		// Notification to potentially update, or create, a user
		// We should not receive anything on this channel if we're in Simple Mode
		// case user := <-b.updateUserChan:
		// 	b.IRCPuppeteer.HandleUser(user)

		// case userID := <-b.removeUserChan:
		// 	b.IRCPuppeteer.DisconnectUser(userID)

		// Done!
		case <-b.done:
			b.discord.Close()
			b.ircListener.Quit()
			b.IRCPuppeteer.Close()
			close(b.done)

			return
		}

	}
}
