package bridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

type CdnRefresherHttpServer struct {
	discord *discordgo.Session
}

type DiscordApiRefreshLinksResponse struct {
	RefreshedUrls []struct {
		Original  string `json:"original"`
		Refreshed string `json:"refreshed"`
	} `json:"refreshed_urls"`
}

const DiscordApiRefreshUrlsEndpoint = "https://discord.com/api/v10/attachments/refresh-urls"

func (s CdnRefresherHttpServer) apiRequestRefreshLinks(cdnLinks []string) (st DiscordApiRefreshLinksResponse, err error) {
	var requestBody = map[string]interface{}{
		"attachment_urls": cdnLinks,
	}
	response, err := s.discord.RequestWithBucketID("GET", DiscordApiRefreshUrlsEndpoint, requestBody, DiscordApiRefreshUrlsEndpoint)
	if err != nil {
		return
	}

	err = json.Unmarshal(response, &st)
	if err != nil {
		return
	}

	return
}

func (s CdnRefresherHttpServer) handleRefreshLink(w http.ResponseWriter, req *http.Request) {
	if !strings.HasPrefix(req.URL.Path, "https://cdn.discordapp.com") {
		http.Error(w, "Invalid CDN URL", http.StatusBadRequest)
		return
	}

	res, err := s.apiRequestRefreshLinks([]string{req.URL.Path})
	if err != nil {
		http.Error(w, "Discord API error", http.StatusBadGateway)
		return
	}

	refreshedLink := res.RefreshedUrls[0].Refreshed
	refreshedUrl, err := url.Parse(refreshedLink)
	if err != nil {
		q := refreshedUrl.Query()
		expires := q.Get("ex")
		// Not needed right now
		// issued := q.Get("is")
		// sig := q.Get("hm")
		expiresUnix, err := strconv.ParseInt(expires, 16, 64)
		if err != nil {
			expires := time.Unix(expiresUnix, 0)
			timeLeft := int64(time.Until(expires).Seconds())
			w.Header().Set("Cache-Control", strconv.FormatInt(timeLeft, 10))
		}
	}

	http.Redirect(w, req, refreshedLink, http.StatusTemporaryRedirect)
}

func (s CdnRefresherHttpServer) start(listenAddr string) error {
	server := http.Server{
		Addr:    listenAddr,
		Handler: http.HandlerFunc(s.handleRefreshLink),
	}
	err := server.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
