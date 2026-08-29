package bridge

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/42wim/matterbridge/bridge/discord/transmitter"
	"github.com/qaisjp/go-discord-irc/dstate"

	"github.com/bwmarrin/discordgo"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type discordCache struct {
	// guild ID -> username -> *Member object
	// the cache provided by discordgo is from user ID, which isn't helpful for us handling user mentions.
	membersCache map[string]map[string]*discordgo.Member
}

func (dc *discordCache) getMember(guildID, userID string) *discordgo.Member {
	guild, ok := dc.membersCache[guildID]
	if !ok {
		return nil
	}
	return guild[userID]
}

func (dc *discordCache) onMemberListChunk(s *discordgo.Session, m *discordgo.GuildMembersChunk) {
	guild, ok := dc.membersCache[m.GuildID]
	if !ok {
		guild = make(map[string]*discordgo.Member)
		dc.membersCache[m.GuildID] = guild
	}

	for _, member := range m.Members {
		guild[member.User.Username] = member
	}
}

func (dc *discordCache) onMemberUpdate(s *discordgo.Session, m *discordgo.GuildMemberUpdate) {
	guild, ok := dc.membersCache[m.GuildID]
	if !ok {
		return
	}

	guild[m.User.Username] = m.Member
}

func (dc *discordCache) onMemberLeave(s *discordgo.Session, m *discordgo.GuildMemberRemove) {
	guild, ok := dc.membersCache[m.GuildID]
	if !ok {
		return
	}

	guild[m.User.Username] = nil
}

type discordBot struct {
	Session *discordgo.Session
	bridge  *Bridge

	guildID string

	transmitter *transmitter.Transmitter

	cache discordCache
}

func newDiscord(bridge *Bridge, botToken, guildID string) (*discordBot, error) {

	// Create a new Discord session using the provided bot token.
	session, err := discordgo.New("Bot " + botToken)
	if err != nil {
		return nil, errors.Wrap(err, "discord, could not create new session")
	}
	session.StateEnabled = true

	discord := &discordBot{
		Session: session,
		bridge:  bridge,

		guildID: guildID,

		cache: discordCache{
			membersCache: make(map[string]map[string]*discordgo.Member),
		},
	}
	dc := discord.cache

	// These events are all fired in separate goroutines
	discord.Session.AddHandler(discord.onReady)
	discord.Session.AddHandler(discord.onMessageCreate)
	discord.Session.AddHandler(discord.onMessageUpdate)
	discord.Session.AddHandler(discord.onGuildEmojiUpdate)

	discord.Session.AddHandler(discord.onMemberListChunk)
	discord.Session.AddHandler(discord.onMemberUpdate)
	discord.Session.AddHandler(discord.onMemberLeave)
	discord.Session.AddHandler(discord.onPresencesReplace)
	discord.Session.AddHandler(discord.onPresenceUpdate)
	discord.Session.AddHandler(discord.onTypingStart)
	discord.Session.AddHandler(discord.onMessageReactionAdd)

	discord.Session.AddHandler(dc.onMemberListChunk)
	discord.Session.AddHandler(dc.onMemberUpdate)
	discord.Session.AddHandler(dc.onMemberLeave)

	return discord, nil
}

func (d *discordBot) Open() error {
	d.transmitter = transmitter.New(d.Session, d.guildID, "irc-bridge", true)
	d.transmitter.Log = log.NewEntry(log.StandardLogger())
	if err := d.transmitter.RefreshGuildWebhooks(nil); err != nil {
		return fmt.Errorf("failed to refresh guild webhooks: %w", err)
	}

	d.Session.Identify.Intents = discordgo.MakeIntent(discordgo.IntentsAll)
	err := d.Session.Open()
	if err != nil {
		return errors.Wrap(err, "discord, could not open session")
	}

	return nil
}

func (d *discordBot) Close() error {
	return errors.Wrap(d.Session.Close(), "closing discord session")
}

// Returns `<@uid>` if a discord user or just `name` if a bot
func userToMention(u *discordgo.User) (mention string) {
	mention = u.Username
	if !u.Bot {
		mention = u.Mention()
	}
	return
}

// For spoiler colouring:
var spoilerPattern = regexp.MustCompile(`\|\|(.*?)\|\|`)
var colorCode = string(rune(3))

var cdnDiscordAppURL = regexp.MustCompile(`(\d+)\/(\d+)\/([^?]+)\?`)

func cleanupCdnDiscordAppURL(url string) string {
	matches := cdnDiscordAppURL.FindStringSubmatch(url)
	if len(matches) != 3 {
		return ""
	}
	// guildID-channelID-filename
	return matches[1] + "-" + matches[2] + "-" + matches[3]
}

func reuploadAttachment(rustypasteURL, rustypasteToken, sourceURL string) string {
	var reqBody bytes.Buffer
	w := multipart.NewWriter(&reqBody)
	w.WriteField("remote", sourceURL)
	w.Close()

	req, err := http.NewRequest(http.MethodPost, rustypasteURL, &reqBody)
	if err != nil {
		log.Errorln(err)
		return sourceURL
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", rustypasteToken)

	reuploadFilename := cleanupCdnDiscordAppURL(sourceURL)
	if reuploadFilename == "" {
		log.Errorln("attachment file name parse failed")
		return sourceURL
	}
	req.Header.Set("filename", reuploadFilename)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Errorln(err)
		return sourceURL
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Errorln(err)
		return sourceURL
	}
	if resp.StatusCode != http.StatusOK {
		log.Errorln("rustypatse upload failed", resp.Status, respBody)
		return sourceURL
	}

	newURL := string(respBody)
	newURL = strings.TrimSpace(newURL)
	return newURL
}

func (d *discordBot) publishMessage(s *discordgo.Session, m *discordgo.Message, wasEdit bool) {
	// Fix crash if these fields don't exist
	if m.Author == nil || s.State.User == nil {
		// todo: add sentry logging
		return
	}

	// Ignore all messages created by the bot itself
	if m.Author.ID == s.State.User.ID {
		return
	}

	// Ignore messages sent from our webhooks
	if d.transmitter.HasWebhook(m.Author.ID) {
		return
	}

	// If the message is "ping" reply with "Pong!"
	if m.Content == "ping" {
		_, err := s.ChannelMessageSend(m.ChannelID, "Pong!")
		if err != nil {
			log.Warningln("Could not respond to Discord ping message", err.Error())
		}
	}

	// HACK: this is before d.ParseText so that the existing <@uid> translation logic can be used
	if m.MessageReference != nil &&
		m.MessageReference.Type == discordgo.MessageReferenceTypeDefault &&
		m.MessageReference.ChannelID == m.ChannelID {
		// Fallback prefix
		prefix := "[reply]"
		// If we can fetch the replied-to message, use ideal prefix. Make it look like a mention.
		msg, err := dstate.ChannelMessage(d.Session, m.MessageReference.ChannelID, m.MessageReference.MessageID)
		if err == nil {
			prefix = userToMention(msg.Author) + ":"
			if !msg.Author.Bot {
				// HACK: theoretically could already be there, thereotically not a big problem
				m.Mentions = append(m.Mentions, msg.Author)
			}
		}
		m.Content = prefix + " " + m.Content
	}

	if m.MessageReference != nil &&
		m.MessageReference.Type == discordgo.MessageReferenceTypeForward {
		// Fallback prefix
		var prefix strings.Builder
		prefix.WriteString("[forwarded]")
		// If we can fetch the forwarded message, use its source
		sourceGuild, err := d.Session.State.Guild(m.MessageReference.GuildID)
		if err == nil {
			prefix.Reset()
			prefix.WriteString("forwarded from ")
			prefix.WriteString(sourceGuild.Name)
			sourceChannel, err := d.Session.State.Channel(m.MessageReference.ChannelID)
			if err == nil {
				prefix.WriteRune(' ')
				prefix.WriteString(sourceChannel.Name)
			}
		}

		prefix.WriteString(":\n")

		// https://discord.com/developers/docs/resources/message#message-snapshot-object
		// * The current subset of message fields consists of: type, content, embeds, attachments, timestamp, edited_timestamp, flags, mentions, mention_roles, stickers, sticker_items, and components.

		// Copy over all relevant fields from the referenced message
		lastSnapshot := m.MessageSnapshots[len(m.MessageSnapshots)-1].Message
		prefix.WriteString(lastSnapshot.Content) // "prefix" is a misnomer at this point, we're using the Builder to construct the whole updated message content.
		m.Content = prefix.String()
		m.Embeds = lastSnapshot.Embeds
		m.Attachments = lastSnapshot.Attachments
		m.Timestamp = lastSnapshot.Timestamp
		m.EditedTimestamp = lastSnapshot.EditedTimestamp
		m.Mentions = lastSnapshot.Mentions
		m.MentionRoles = lastSnapshot.MentionRoles
		m.StickerItems = lastSnapshot.StickerItems
		m.Components = lastSnapshot.Components
	}

	content := d.ParseText(m)

	// The content is an action if it matches "_(.+)_"
	isAction := len(content) > 2 &&
		m.Content[0] == '_' &&
		m.Content[len(content)-1] == '_'

	// If it is an action, remove the enclosing underscores
	if isAction {
		content = content[1 : len(m.Content)-1]
	}

	if wasEdit {
		if isAction {
			content = "/me " + content
		}

		content = "[edit] " + content
	}

	if strings.Count(content, "||") >= 2 {
		content = spoilerPattern.ReplaceAllString(content, colorCode+"1,1$1"+colorCode)
	}

	d.bridge.discord2ircChan <- &DiscordMessage{
		Message:  m,
		Content:  content,
		IsAction: isAction,
	}

	pastebinURL := d.bridge.Config.PastebinURL
	pastebinToken := d.bridge.Config.PastebinToken
	for _, attachment := range m.Attachments {
		res := reuploadAttachment(pastebinURL, pastebinToken, attachment.URL)
		d.bridge.discord2ircChan <- &DiscordMessage{
			Message:  m,
			Content:  res,
			IsAction: isAction,
		}
	}
}

func (d *discordBot) publishReaction(s *discordgo.Session, r *discordgo.MessageReaction) {
	if s.State.User == nil {
		return
	}

	user, err := s.User(r.UserID)
	if err != nil {
		log.Errorln(err)
		return
	}

	// Bridge needs these for mapping
	m := &discordgo.Message{
		ChannelID: r.ChannelID,
		Author:    user,
		GuildID:   r.GuildID,
	}

	originalMessage, err := dstate.ChannelMessage(d.Session, r.ChannelID, r.MessageID)
	reactionTarget := ""
	if err == nil {
		// TODO 1: could add extra logic to figure out what length is needed to disambiguate
		// TODO 2: length should not cause command to exceed the max command length

		// HACK: this is before d.ParseText so that the existing <@uid> translation logic can be used
		username := userToMention(originalMessage.Author)
		if !originalMessage.Author.Bot {
			// HACK: theoretically could already be there, thereotically not a big problem
			originalMessage.Mentions = append(originalMessage.Mentions, originalMessage.Author)
		}
		originalMessage.Content = fmt.Sprintf(
			" to <%s> %s",
			username,
			// Truncate messages to just 40 characters so reactions to long messages
			// don't pollute the IRC log. Similarly, replace newlines with spaces
			// so that any reactions to messages with a newline within the first 40
			// characters don't cause multiple IRC messages to be sent.
			strings.ReplaceAll(TruncateString(40, originalMessage.Content), "\n", " "),
		)

		reactionTarget = d.ParseText(originalMessage)
	}

	emoji := r.Emoji.Name
	if r.Emoji.ID != "" {
		// Custom emoji
		emoji = fmt.Sprint(":", emoji, ":")
	}
	content := fmt.Sprint("reacted with ", emoji, reactionTarget)

	d.bridge.discord2ircChan <- &DiscordMessage{
		Message:  m,
		Content:  content,
		IsAction: true,
	}
}

// Up to date as of https://git.io/v5kJg
var channelMention = regexp.MustCompile(`<#(\d+)>`)
var roleMention = regexp.MustCompile(`<@&(\d+)>`)

var patternChannels = regexp.MustCompile("<#[^>]*>")
var emoteRegex = regexp.MustCompile(`<a?(:\w+:)\d+>`)

// Up to date as of https://git.io/v5kJg
func (d *discordBot) ParseText(m *discordgo.Message) string {
	content := m.Content

	//////////////
	// Literal replacements

	replacements := []string{}

	// Break down malformed newlines
	replacements = append(replacements,
		"\r\n", "\n", // replace CRLF with LF
		"\r", "\n", // replace CR with LF
	)

	// Replace @user mentions with name~d mentions
	for _, user := range m.Mentions {
		ircNick := d.bridge.IRCPuppeteer.generateNickname(user)
		replacements = append(replacements,
			"<@"+user.ID+">", ircNick,
			"<@!"+user.ID+">", ircNick)
	}

	// Copied from message.go ContentWithMoreMentionsReplaced(s)
	for _, roleID := range m.MentionRoles {
		role, err := d.Session.State.Role(d.guildID, roleID)
		if err != nil || !role.Mentionable {
			continue
		}

		replacements = append(replacements, "<&"+role.ID+">", "@"+role.Name)
	}

	content = strings.NewReplacer(replacements...).Replace(content)

	//////////////
	// Regex and custom logic replacements

	// Also copied from message.go ContentWithMoreMentionsReplaced(s)
	content = patternChannels.ReplaceAllStringFunc(content, func(mention string) string {
		channel, err := d.Session.State.Channel(mention[2 : len(mention)-1])
		if err != nil || channel.Type == discordgo.ChannelTypeGuildVoice {
			return mention
		}

		return "#" + channel.Name
	})

	// Replace <#xxxxx> channel mentions
	content = channelMention.ReplaceAllStringFunc(content, func(str string) string {
		// Strip enclosing identifiers
		channelID := str[2 : len(str)-1]

		channel, err := d.Session.State.Channel(channelID)
		if err == nil {
			return "#" + channel.Name
		} else if err == discordgo.ErrStateNotFound {
			return "#deleted-channel"
		}

		panic(errors.Wrap(err, "Channel mention failed for "+str))
	})

	// Replace <@&xxxxx> role mentions
	content = roleMention.ReplaceAllStringFunc(content, func(str string) string {
		// Strip enclosing identifiers
		roleID := str[3 : len(str)-1]

		role, err := d.Session.State.Role(d.bridge.Config.GuildID, roleID)
		if err == nil {
			return "@" + role.Name
		} else if err == discordgo.ErrStateNotFound {
			return "@deleted-role"
		}

		panic(errors.Wrap(err, "Channel mention failed for "+str))
	})

	// Replace emotes
	content = emoteRegex.ReplaceAllString(content, "$1")

	return content
}

func (d *discordBot) handlePresenceUpdate(uid string, status discordgo.Status, forceOnline bool) {
	// If they are offline, just deliver a mostly empty struct with the ID and online state
	if !forceOnline && !isStatusOnline(status) {
		if d.bridge.Config.DebugPresence {
			log.WithField("id", uid).Debugln("PRESENCE", status, "(handlePresenceUpdate - Online: false)")
		}
		d.sendUpdateUserChan(DiscordUser{
			ID:     uid,
			Online: false,
		})
		return
	}

	if d.bridge.Config.DebugPresence {
		log.WithField("id", uid).Debugln("PRESENCE", status, "(handlePresenceUpdate)")
	}

	// Otherwise get their GuildMember object...
	user, err := d.Session.State.Member(d.guildID, uid)
	if err != nil {
		log.Println(errors.Wrap(err, "get member from state in handlePresenceUpdate failed"))
		return
	}

	// .. and handle as per usual
	d.handleMemberUpdate(user, forceOnline)
}

func isStatusOnline(status discordgo.Status) bool {
	return status != discordgo.StatusOffline
}

func (d *discordBot) sendUpdateUserChan(user DiscordUser) bool {
	// Only log this for online events, because offline events won't have this
	if (user.Username == "") && user.Online {
		log.WithFields(log.Fields{
			"err":           errors.WithStack(errors.New("empty username")).Error(),
			"user.Username": user.Username,
			"user.ID":       user.ID,
		}).Println("sendUpdateUserChan called with empty Username (see stack below)")
		debug.PrintStack()
	}

	d.bridge.discordUpdateUserChan <- user
	return true
}

// Get URL to a Discord member's avatar, based on their username.
// Note the username is expected to be formatted properly according to https://support.discord.com/hc/en-us/articles/12620128861463-New-Usernames-Display-Names
//
// See https://github.com/reactiflux/discord-irc/pull/230/files#diff-7202bb7fb017faefd425a2af32df2f9dR357
func (d *discordBot) GetAvatar(guildID, username string) (_ string) {
	member := d.cache.getMember(guildID, username)
	if member == nil {
		return
	}

	return discordgo.EndpointUserAvatar(member.User.ID, member.User.Avatar)
}

func (d *discordBot) GetUserID(guildID, username string) string {
	member := d.cache.getMember(guildID, username)
	if member == nil {
		return ""
	}

	return member.User.ID
}

// GetMemberNick returns the real display name for a Discord GuildMember
func GetMemberNick(m *discordgo.Member) string {
	if m.Nick == "" {
		return m.User.Username
	}

	return m.Nick
}

func (d *discordBot) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	d.publishMessage(s, m.Message, false)
}

func (d *discordBot) onMessageUpdate(s *discordgo.Session, m *discordgo.MessageUpdate) {
	d.publishMessage(s, m.Message, true)
}

func (d *discordBot) onMessageReactionAdd(s *discordgo.Session, m *discordgo.MessageReactionAdd) {
	d.publishReaction(s, m.MessageReaction)
}

// onMemberListChunk is fired in response to our GuildMembers request in onReady
func (d *discordBot) onMemberListChunk(s *discordgo.Session, m *discordgo.GuildMembersChunk) {
	for _, m := range m.Members {
		d.handleMemberUpdate(m, false)
	}
}

func (d *discordBot) onMemberUpdate(s *discordgo.Session, m *discordgo.GuildMemberUpdate) {
	d.handleMemberUpdate(m.Member, false)
}

// onMemberLeave is triggered when a user is removed from a guild (leave/kick/ban).
func (d *discordBot) onMemberLeave(s *discordgo.Session, m *discordgo.GuildMemberRemove) {
	d.bridge.discordRemoveUserChan <- m.User.ID
}

// What does this do? Probably what it sounds like.
func (d *discordBot) onPresencesReplace(s *discordgo.Session, m *discordgo.PresencesReplace) {
	for _, p := range *m {
		d.handlePresenceUpdate(p.User.ID, p.Status, false)
	}
}

// Handle when presence is updated
func (d *discordBot) onPresenceUpdate(s *discordgo.Session, m *discordgo.PresenceUpdate) {
	d.handlePresenceUpdate(m.Presence.User.ID, m.Presence.Status, false)
}

func (d *discordBot) onTypingStart(s *discordgo.Session, m *discordgo.TypingStart) {
	status := discordgo.StatusOffline

	p, err := d.Session.State.Presence(d.guildID, m.UserID)
	if err != nil {
		log.Println(errors.Wrap(err, "get presence from in onTypingStart failed"))
		// return
	} else {
		status = p.Status
	}

	// .. and handle as per usual
	d.handlePresenceUpdate(m.UserID, status, true)
}

func (d *discordBot) onReady(s *discordgo.Session, m *discordgo.Ready) {
	// Fires a GuildMembersChunk event
	err := d.Session.RequestGuildMembers(d.guildID, "", 0, "", true)
	if err != nil {
		log.Warningln(errors.Wrap(err, "could not request guild members").Error())
		return
	}

	emoji, err := d.Session.GuildEmojis(d.guildID)
	if err == nil {
		d.setGuildEmoji(d.guildID, emoji)
	}
}

func (d *discordBot) onGuildEmojiUpdate(s *discordgo.Session, m *discordgo.GuildEmojisUpdate) {
	d.setGuildEmoji(m.GuildID, m.Emojis)
}

func (d *discordBot) setGuildEmoji(guild string, emoji []*discordgo.Emoji) {
	d.bridge.emoji = make(map[string]*discordgo.Emoji)
	for _, e := range emoji {
		d.bridge.emoji[strings.ToLower(e.Name)] = e
	}
}

func (d *discordBot) handleMemberUpdate(m *discordgo.Member, forceOnline bool) {
	status := discordgo.StatusOnline

	if !forceOnline {
		presence, err := d.Session.State.Presence(d.guildID, m.User.ID)
		if err != nil {
			// This error is usually triggered on first run because it represents offline
			if err != discordgo.ErrStateNotFound {
				log.WithField("error", err).Errorln("presence retrieval failed")
			}
			return
		}

		if !isStatusOnline(presence.Status) {
			return
		}

		status = presence.Status
	}

	d.sendUpdateUserChan(DiscordUser{
		ID:       m.User.ID,
		Username: m.User.Username,
		Nick:     GetMemberNick(m),
		Bot:      m.User.Bot,
		Online:   isStatusOnline(status),
	})
}
