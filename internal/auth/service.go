package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Tanish431/worduel/internal/middleware"
	"github.com/Tanish431/worduel/internal/models"
	"github.com/Tanish431/worduel/pkg/config"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
	localProvider     = "local"
	googleProvider    = "google"
)

type Service struct {
	db                 *pgxpool.Pool
	jwtSecret          string
	frontendOrigin     string
	googleClientID     string
	googleClientSecret string
	googleRedirectURL  string
	httpClient         *http.Client
}

func NewService(db *pgxpool.Pool, cfg *config.Config) *Service {
	return &Service{
		db:                 db,
		jwtSecret:          cfg.JWTSecret,
		frontendOrigin:     strings.TrimRight(cfg.FrontendOrigin, "/"),
		googleClientID:     cfg.GoogleClientID,
		googleClientSecret: cfg.GoogleClientSecret,
		googleRedirectURL:  cfg.GoogleRedirectURL,
		httpClient:         &http.Client{Timeout: 10 * time.Second},
	}
}

type registerRequest struct {
	Username string `json:"username" binding:"required"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
}

type loginRequest struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type googleTokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
}

type googleUserInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

type googleStateClaims struct {
	RedirectURI string `json:"redirect_uri"`
	jwt.RegisteredClaims
}

func (s *Service) HandleRegister(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	user := models.User{
		ID:           uuid.New(),
		Username:     req.Username,
		Email:        strings.ToLower(req.Email),
		PasswordHash: string(hash),
		AuthProvider: localProvider,
		ELO:          1000,
		CreatedAt:    time.Now(),
	}

	_, err = s.db.Exec(c.Request.Context(),
		`INSERT INTO users (id, username, email, password_hash, auth_provider, elo, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		user.ID, user.Username, user.Email, user.PasswordHash, user.AuthProvider, user.ELO, user.CreatedAt,
	)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "username or email already taken"})
		return
	}

	token, err := s.issueToken(user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"token": token, "user": user})
}

func (s *Service) HandleLogin(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var user models.User
	err := s.db.QueryRow(c.Request.Context(),
		`SELECT id, username, email, password_hash, auth_provider, provider_user_id, elo, created_at
		FROM users WHERE email=$1`,
		strings.ToLower(req.Email),
	).Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash, &user.AuthProvider, &user.ProviderUserID, &user.ELO, &user.CreatedAt)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	if user.AuthProvider != "" && user.AuthProvider != localProvider {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "this account uses Google sign-in"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	token, err := s.issueToken(user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token, "user": user})
}

func (s *Service) HandleGoogleLogin(c *gin.Context) {
	if !s.googleOAuthConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "google oauth is not configured"})
		return
	}

	frontendRedirectURI := c.Query("redirect_uri")
	if !s.isAllowedFrontendRedirect(frontendRedirectURI) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid redirect_uri"})
		return
	}

	state, err := s.issueGoogleState(frontendRedirectURI)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start google oauth"})
		return
	}

	params := url.Values{}
	params.Set("client_id", s.googleClientID)
	params.Set("redirect_uri", s.googleRedirectURL)
	params.Set("response_type", "code")
	params.Set("scope", "openid email profile")
	params.Set("state", state)
	params.Set("access_type", "online")
	params.Set("prompt", "select_account")

	c.Redirect(http.StatusTemporaryRedirect, "https://accounts.google.com/o/oauth2/v2/auth?"+params.Encode())
}

func (s *Service) HandleGoogleCallback(c *gin.Context) {
	frontendRedirectURI, err := s.parseGoogleState(c.Query("state"))
	if err != nil {
		c.Redirect(http.StatusTemporaryRedirect, s.buildFrontendErrorRedirect(s.frontendOrigin+"/auth/google/callback", "invalid oauth state"))
		return
	}

	if oauthError := c.Query("error"); oauthError != "" {
		c.Redirect(http.StatusTemporaryRedirect, s.buildFrontendErrorRedirect(frontendRedirectURI, oauthError))
		return
	}

	code := c.Query("code")
	if code == "" {
		c.Redirect(http.StatusTemporaryRedirect, s.buildFrontendErrorRedirect(frontendRedirectURI, "missing oauth code"))
		return
	}

	tokenResp, err := s.exchangeGoogleCode(c.Request.Context(), code)
	if err != nil {
		c.Redirect(http.StatusTemporaryRedirect, s.buildFrontendErrorRedirect(frontendRedirectURI, "google token exchange failed"))
		return
	}

	googleUser, err := s.fetchGoogleUserInfo(c.Request.Context(), tokenResp.AccessToken)
	if err != nil {
		c.Redirect(http.StatusTemporaryRedirect, s.buildFrontendErrorRedirect(frontendRedirectURI, "failed to fetch google profile"))
		return
	}

	if googleUser.Email == "" || !googleUser.EmailVerified {
		c.Redirect(http.StatusTemporaryRedirect, s.buildFrontendErrorRedirect(frontendRedirectURI, "google account email is not verified"))
		return
	}

	user, err := s.findOrCreateGoogleUser(c.Request.Context(), googleUser)
	if err != nil {
		c.Redirect(http.StatusTemporaryRedirect, s.buildFrontendErrorRedirect(frontendRedirectURI, "failed to sign in with google"))
		return
	}

	token, err := s.issueToken(user.ID)
	if err != nil {
		c.Redirect(http.StatusTemporaryRedirect, s.buildFrontendErrorRedirect(frontendRedirectURI, "failed to create session"))
		return
	}

	redirectURL, err := url.Parse(frontendRedirectURI)
	if err != nil {
		c.Redirect(http.StatusTemporaryRedirect, s.buildFrontendErrorRedirect(s.frontendOrigin+"/auth/google/callback", "invalid frontend redirect"))
		return
	}

	query := redirectURL.Query()
	query.Set("token", token)
	redirectURL.RawQuery = query.Encode()
	c.Redirect(http.StatusTemporaryRedirect, redirectURL.String())
}

func (s *Service) HandleGetMe(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var user models.User
	err := s.db.QueryRow(c.Request.Context(),
		`SELECT id, username, email, elo, created_at From users WHERE id=$1`, userID,
	).Scan(&user.ID, &user.Username, &user.Email, &user.ELO, &user.CreatedAt)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	c.JSON(http.StatusOK, user)
}

func (s *Service) googleOAuthConfigured() bool {
	return s.googleClientID != "" && s.googleClientSecret != "" && s.googleRedirectURL != ""
}

func (s *Service) issueToken(userID uuid.UUID) (string, error) {
	claims := jwt.MapClaims{
		"sub": userID.String(),
		"exp": time.Now().Add(7 * 24 * time.Hour).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.jwtSecret))
}

func (s *Service) issueGoogleState(frontendRedirectURI string) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	claims := googleStateClaims{
		RedirectURI: frontendRedirectURI,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        base64.RawURLEncoding.EncodeToString(nonce),
		},
	}

	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.jwtSecret))
}

func (s *Service) parseGoogleState(state string) (string, error) {
	if state == "" {
		return "", errors.New("missing state")
	}

	token, err := jwt.ParseWithClaims(state, &googleStateClaims{}, func(token *jwt.Token) (any, error) {
		return []byte(s.jwtSecret), nil
	})
	if err != nil {
		return "", err
	}

	claims, ok := token.Claims.(*googleStateClaims)
	if !ok || !token.Valid {
		return "", errors.New("invalid state claims")
	}

	if !s.isAllowedFrontendRedirect(claims.RedirectURI) {
		return "", errors.New("invalid frontend redirect")
	}

	return claims.RedirectURI, nil
}

func (s *Service) isAllowedFrontendRedirect(redirectURI string) bool {
	if redirectURI == "" {
		return false
	}

	redirectURL, err := url.Parse(redirectURI)
	if err != nil {
		return false
	}

	origin := redirectURL.Scheme + "://" + redirectURL.Host
	return origin == s.frontendOrigin
}

func (s *Service) exchangeGoogleCode(ctx context.Context, code string) (*googleTokenResponse, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", s.googleClientID)
	form.Set("client_secret", s.googleClientSecret)
	form.Set("redirect_uri", s.googleRedirectURL)
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("google token exchange failed with status %d", res.StatusCode)
	}

	var tokenResp googleTokenResponse
	if err := json.NewDecoder(res.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	if tokenResp.AccessToken == "" {
		return nil, errors.New("missing access token")
	}

	return &tokenResp, nil
}

func (s *Service) fetchGoogleUserInfo(ctx context.Context, accessToken string) (*googleUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	res, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("google userinfo failed with status %d", res.StatusCode)
	}

	var info googleUserInfo
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
		return nil, err
	}

	return &info, nil
}

func (s *Service) findOrCreateGoogleUser(ctx context.Context, googleUser *googleUserInfo) (*models.User, error) {
	var user models.User
	err := s.db.QueryRow(ctx,
		`SELECT id, username, email, password_hash, auth_provider, provider_user_id, elo, created_at
		FROM users
		WHERE auth_provider = $1 AND provider_user_id = $2`,
		googleProvider, googleUser.Sub,
	).Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash, &user.AuthProvider, &user.ProviderUserID, &user.ELO, &user.CreatedAt)
	if err == nil {
		return &user, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	err = s.db.QueryRow(ctx,
		`SELECT id, username, email, password_hash, auth_provider, provider_user_id, elo, created_at
		FROM users
		WHERE email = $1`,
		strings.ToLower(googleUser.Email),
	).Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash, &user.AuthProvider, &user.ProviderUserID, &user.ELO, &user.CreatedAt)
	if err == nil {
		_, updateErr := s.db.Exec(ctx,
			`UPDATE users
			SET auth_provider = $1, provider_user_id = $2
			WHERE id = $3`,
			googleProvider, googleUser.Sub, user.ID,
		)
		if updateErr != nil {
			return nil, updateErr
		}
		user.AuthProvider = googleProvider
		user.ProviderUserID = &googleUser.Sub
		return &user, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	username, err := s.generateUniqueUsername(ctx, googleUser)
	if err != nil {
		return nil, err
	}

	user = models.User{
		ID:             uuid.New(),
		Username:       username,
		Email:          strings.ToLower(googleUser.Email),
		PasswordHash:   "",
		AuthProvider:   googleProvider,
		ProviderUserID: &googleUser.Sub,
		ELO:            1000,
		CreatedAt:      time.Now(),
	}

	_, err = s.db.Exec(ctx,
		`INSERT INTO users (id, username, email, password_hash, auth_provider, provider_user_id, elo, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		user.ID, user.Username, user.Email, user.PasswordHash, user.AuthProvider, user.ProviderUserID, user.ELO, user.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	return &user, nil
}

func (s *Service) generateUniqueUsername(ctx context.Context, googleUser *googleUserInfo) (string, error) {
	base := sanitizeUsername(googleUser.Name)
	if base == "" {
		base = sanitizeUsername(strings.Split(googleUser.Email, "@")[0])
	}
	if base == "" {
		base = "player"
	}
	if len(base) > 20 {
		base = base[:20]
	}

	candidate := base
	for i := 0; i < 20; i++ {
		var exists bool
		err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE username = $1)`, candidate).Scan(&exists)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s%d", base, i+2)
	}

	return fmt.Sprintf("%s%d", base, time.Now().Unix()%100000), nil
}

func sanitizeUsername(input string) string {
	input = strings.ToLower(strings.TrimSpace(input))
	var builder strings.Builder
	for _, r := range input {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_' || r == '-':
			builder.WriteRune(r)
		case r == ' ' || r == '.':
			builder.WriteRune('_')
		}
	}
	return strings.Trim(builder.String(), "_-")
}

func (s *Service) buildFrontendErrorRedirect(frontendRedirectURI, message string) string {
	redirectURL, err := url.Parse(frontendRedirectURI)
	if err != nil {
		redirectURL, _ = url.Parse(s.frontendOrigin + "/auth/google/callback")
	}
	query := redirectURL.Query()
	query.Set("error", message)
	redirectURL.RawQuery = query.Encode()
	return redirectURL.String()
}
