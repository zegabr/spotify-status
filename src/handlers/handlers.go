package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/o-mago/spotify-status/src/app_error"
	"github.com/o-mago/spotify-status/src/domain"
	"github.com/o-mago/spotify-status/src/services"
	"github.com/zmb3/spotify"
)

type handlers struct {
	services             services.Services
	spotifyAuthenticator spotify.Authenticator
	spotifyState         string
	slackClientID        string
	slackClientSecret    string
	slackAuthURL         string
}

type Handlers interface {
	HealthHandler(w http.ResponseWriter, r *http.Request)
	SpotifyCallbackHandler(w http.ResponseWriter, r *http.Request)
	SlackCallbackHandler(w http.ResponseWriter, r *http.Request)

	writeResponse(w http.ResponseWriter, resp interface{}, status int)
}

func NewHandlers(services services.Services, spotifyAuthenticator spotify.Authenticator, spotifyState, slackClientID, slackClientSecret, slackAuthURL string) Handlers {
	return handlers{
		services,
		spotifyAuthenticator,
		spotifyState,
		slackClientID,
		slackClientSecret,
		slackAuthURL,
	}
}

func (h handlers) writeResponse(w http.ResponseWriter, resp interface{}, status int) {
	w.WriteHeader(status)
	w.Header().Set("Content-Type", "application/json")
	jsonResp, err := json.Marshal(resp)
	if err != nil {
		log.Fatalf("Error happened in JSON marshal. Err: %s", err)
	}
	w.Write(jsonResp)
}

func (h handlers) HealthHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("New Relic ok")
	fmt.Fprintf(w, "OK")
}

func (h handlers) SpotifyCallbackHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID, err := r.Cookie("user_id")
	if err != nil {
		appError := app_error.InvalidCookie
		fmt.Println(err, appError)
		h.writeResponse(w, appError.Error(), appError.Status())
		return
	}
	slackAccessToken, err := r.Cookie("slack_access_token")
	if err != nil {
		appError := app_error.InvalidCookie
		fmt.Println(err, appError)
		h.writeResponse(w, appError.Error(), appError.Status())
		return
	}

	spotifyToken, err := h.spotifyAuthenticator.Token(h.spotifyState, r)
	if err != nil {
		appError := app_error.InvalidSpotifyAuthCode
		fmt.Println(err, appError)
		h.writeResponse(w, appError.Error(), appError.Status())
		return
	}

	user := domain.User{
		SlackUserID:         userID.Value,
		SlackAccessToken:    slackAccessToken.Value,
		SpotifyAccessToken:  spotifyToken.AccessToken,
		SpotifyRefreshToken: spotifyToken.RefreshToken,
		SpotifyExpiry:       spotifyToken.Expiry,
		SpotifyTokenType:    spotifyToken.TokenType,
	}

	err = h.services.AddUser(ctx, user)
	if err != nil {
		appError := app_error.AddUserError
		fmt.Println(err, appError)
		h.writeResponse(w, appError.Error(), appError.Status())
		return
	}

	http.ServeFile(w, r, "./static/completed/index.html")
}

func (h handlers) SlackCallbackHandler(w http.ResponseWriter, r *http.Request) {
	slackCode := r.URL.Query().Get("code")

	requestBody := url.Values{}
	requestBody.Set("code", slackCode)
	requestBody.Set("client_id", h.slackClientID)
	requestBody.Set("client_secret", h.slackClientSecret)

	resp, err := http.Post(h.slackAuthURL, "application/x-www-form-urlencoded", strings.NewReader(requestBody.Encode()))
	if err != nil {
		appError := app_error.SlackAuthBadRequest
		fmt.Println(err, appError)
		h.writeResponse(w, appError.Error(), appError.Status())
		return
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		appError := app_error.SlackAuthBadRequest
		fmt.Println(err, appError)
		h.writeResponse(w, appError.Error(), appError.Status())
		return
	}

	var slackAuthResponse struct {
		Ok         bool   `json:"ok"`
		AppId      string `json:"app_id"`
		AuthedUser struct {
			Id          string `json:"id"`
			Scope       string `json:"scope"`
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
		} `json:"authed_user"`
		Team struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		} `json:"team"`
		Enterprise string `json:"enterprise"`
	}
	err = json.Unmarshal(body, &slackAuthResponse)
	if err != nil {
		appError := app_error.SlackAuthBadRequest
		fmt.Println(err, appError)
		h.writeResponse(w, appError.Error(), appError.Status())
		return
	}

	expiration := time.Now().Add(1 * time.Hour)
	cookieUser := http.Cookie{Name: "user_id", Value: slackAuthResponse.AuthedUser.Id, Expires: expiration}
	cookieSlack := http.Cookie{Name: "slack_access_token", Value: slackAuthResponse.AuthedUser.AccessToken, Expires: expiration}
	http.SetCookie(w, &cookieUser)
	http.SetCookie(w, &cookieSlack)

	spotifyAuthURL := h.spotifyAuthenticator.AuthURL(h.spotifyState)

	http.Redirect(w, r, spotifyAuthURL, http.StatusSeeOther)
}
