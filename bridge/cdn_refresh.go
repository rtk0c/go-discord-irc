package bridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// TODO fix cache stampede
// Currently self-use, and the request volume is low enough I don't consider it a problem

type cdnCache struct {
	expiresAt time.Time
	url       string
}

type cdnRefresherHttpServer struct {
	Discord *discordgo.Session

	// CDN url path (excluding query params) ->
	cache     map[string]cdnCache
	cacheLock sync.RWMutex
}

type DiscordApiRefreshLinksResponse struct {
	RefreshedUrls []struct {
		Original  string `json:"original"`
		Refreshed string `json:"refreshed"`
	} `json:"refreshed_urls"`
}

const DiscordApiRefreshUrlsEndpoint = "https://discord.com/api/v10/attachments/refresh-urls"

func (s *cdnRefresherHttpServer) ApiRefreshLinks(cdnLinks []string) (st DiscordApiRefreshLinksResponse, err error) {
	var requestBody = map[string]interface{}{
		"attachment_urls": cdnLinks,
	}
	response, err := s.Discord.RequestWithBucketID("POST", DiscordApiRefreshUrlsEndpoint, requestBody, DiscordApiRefreshUrlsEndpoint)
	if err != nil {
		return
	}

	err = json.Unmarshal(response, &st)
	if err != nil {
		return
	}

	return
}

func httpCacheControl(w http.ResponseWriter, expiresAt time.Time) {
	timeLeft := int64(time.Until(expiresAt).Seconds())
	w.Header().Set("Cache-Control", "public, max-age="+strconv.FormatInt(timeLeft, 10))
}

func (s *cdnRefresherHttpServer) handleRefreshLink(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get user given path exactly
	// Discord in practice seems to normalize filenames to only contain unreserved characters on upload, and if you try to escape the filename on get, they consider it 404.
	// Let's just throw the error back to the user (thus being defensive against future API changes).
	// (Such a malformed URL will not hit any cache, since nothing can populate it in the first place, since Discord API just errors)
	//
	// If a future API accepts escaped URLs as equivalent, we would just have duplicate cache entries, totally fine.
	// If they allow reserved charactesr on upload without normalizing at all (huh! imagine), it'd still produce the correct behavior, whereas NOT passing exactly would break that.
	//
	// Honestly, why am I thinking so much about this...
	cdnLink := req.URL.RawPath
	if cdnLink == "" {
		cdnLink = req.URL.Path
	}

	if !strings.HasPrefix(cdnLink, "/https://cdn.discordapp.com/") {
		http.Error(w, "Invalid CDN URL", http.StatusBadRequest)
		return
	}
	cdnLink = strings.TrimPrefix(cdnLink, "/")

	s.cacheLock.RLock()
	cacheEntry, exists := s.cache[cdnLink]
	s.cacheLock.RUnlock()
	if exists {
		if time.Now().Before(cacheEntry.expiresAt) {
			httpCacheControl(w, cacheEntry.expiresAt)
			http.Redirect(w, req, cacheEntry.url, http.StatusTemporaryRedirect)
			return
		} else {
			// Concurrency: it is safe to delete() a non-existent two, ok if two goroutines got the same expired cache entry
			//
			// Similarly, these two goroutines would end up refreshing the same URL, which should give us the same result *for all intents and purposes*:
			// (if the API returns one slightly longer lived than the other, so what)
			s.cacheLock.Lock()
			delete(s.cache, cdnLink)
			s.cacheLock.Unlock()
		}
	}

	res, err := s.ApiRefreshLinks([]string{cdnLink})
	if err != nil {
		http.Error(w, "Discord API error", http.StatusBadGateway)
		return
	}

	if len(res.RefreshedUrls) != 1 {
		http.Error(w, "Discord API error", http.StatusBadGateway)
		return
	}

	// Try to set Cache-Control to until the given attachment expires.
	// This way, browsers (given that they don't run out of cache) should stop bothering us until the attachment expires.
	// Also, if a cache reverse proxy sits in front of this server, that can also benefit from not hitting us until the attachment expires.

	refreshedLink := res.RefreshedUrls[0].Refreshed
	refreshedUrl, err := url.Parse(refreshedLink)
	if err == nil {
		q := refreshedUrl.Query()
		expires := q.Get("ex")
		// Not needed right now
		// issued := q.Get("is")
		// sig := q.Get("hm")
		expiresUnix, err := strconv.ParseInt(expires, 16, 64)
		if err == nil {
			expiresAt := time.Unix(expiresUnix, 0)
			httpCacheControl(w, expiresAt)
			http.Redirect(w, req, refreshedLink, http.StatusTemporaryRedirect)

			// Populate cache
			s.cacheLock.Lock()
			s.cache[cdnLink] = cdnCache{
				expiresAt: expiresAt,
				url:       refreshedLink,
			}
			s.cacheLock.Unlock()

			return
		}
	}

	http.Error(w, "Discord API error", http.StatusBadGateway)
}

func (s *cdnRefresherHttpServer) Start(listenAddr string) error {
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
