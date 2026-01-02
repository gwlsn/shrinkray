package password

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gwlsn/shrinkray/internal/auth"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultCookieName = "shrinkray_session"
	defaultSessionTTL = 24 * time.Hour
)

// Provider implements password-based authentication.
type Provider struct {
	users      map[string]string
	hashAlgo   string
	secret     []byte
	cookieName string
	sessionTTL time.Duration
}

// NewProvider creates a new password auth provider.
func NewProvider(users map[string]string, hashAlgo, secret string) (*Provider, error) {
	if len(users) == 0 {
		return nil, errors.New("password auth requires at least one user")
	}
	if secret == "" {
		return nil, errors.New("password auth requires a non-empty secret")
	}
	normalized := strings.ToLower(strings.TrimSpace(hashAlgo))
	if normalized == "" {
		normalized = "auto"
	}
	switch normalized {
	case "auto", "bcrypt", "argon2", "argon2id", "argon2i":
	default:
		return nil, fmt.Errorf("unsupported hash algorithm: %s", hashAlgo)
	}
	return &Provider{
		users:      users,
		hashAlgo:   normalized,
		secret:     []byte(secret),
		cookieName: defaultCookieName,
		sessionTTL: defaultSessionTTL,
	}, nil
}

// Authenticate verifies the session cookie.
func (p *Provider) Authenticate(r *http.Request) (*auth.User, error) {
	cookie, err := r.Cookie(p.cookieName)
	if err != nil {
		return nil, err
	}

	username, expiry, err := p.verifySession(cookie.Value)
	if err != nil {
		return nil, err
	}
	if expiry.Before(time.Now()) {
		return nil, errors.New("session expired")
	}
	if _, ok := p.users[username]; !ok {
		return nil, errors.New("unknown user")
	}

	return &auth.User{ID: username, Name: username}, nil
}

// LoginURL returns the login endpoint.
func (p *Provider) LoginURL(_ *http.Request) (string, error) {
	return "/auth/login", nil
}

// HandleCallback is not used for password auth.
func (p *Provider) HandleCallback(_ http.ResponseWriter, _ *http.Request) error {
	return errors.New("password auth does not support callbacks")
}

// HandleLogin authenticates credentials and issues a session cookie.
func (p *Provider) HandleLogin(w http.ResponseWriter, r *http.Request) error {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`<!doctype html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Shrinkray Login</title>
    <link rel="preconnect" href="https://fonts.googleapis.com">
    <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
    <link rel="icon" type="image/png" href="/favicon.png">
    <link href="https://fonts.googleapis.com/css2?family=DM+Sans:ital,opsz,wght@0,9..40,300;0,9..40,400;0,9..40,500;0,9..40,600;0,9..40,700&display=swap" rel="stylesheet">
    <style>
        :root {
            --bg-primary: #FAFAFA;
            --bg-secondary: #FFFFFF;
            --bg-tertiary: #F5F5F5;
            --text-primary: #1A1A1A;
            --text-secondary: #6B6B6B;
            --text-tertiary: #9A9A9A;
            --accent: #2563EB;
            --accent-hover: #1D4ED8;
            --accent-light: #EFF6FF;
            --border: #E5E5E5;
            --border-hover: #D4D4D4;
            --shadow-md: 0 4px 12px rgba(0,0,0,0.08);
            --radius-sm: 8px;
            --radius-md: 12px;
            --radius-lg: 16px;
            --font-sans: 'DM Sans', -apple-system, BlinkMacSystemFont, sans-serif;
            --transition-fast: 150ms ease;
        }

        [data-theme="dark"] {
            --bg-primary: #0F0F0F;
            --bg-secondary: #1A1A1A;
            --bg-tertiary: #252525;
            --text-primary: #F5F5F5;
            --text-secondary: #A0A0A0;
            --text-tertiary: #6B6B6B;
            --accent: #3B82F6;
            --accent-hover: #60A5FA;
            --accent-light: #1E3A5F;
            --border: #2E2E2E;
            --border-hover: #404040;
            --shadow-md: 0 4px 12px rgba(0,0,0,0.3);
        }

        * {
            box-sizing: border-box;
            margin: 0;
            padding: 0;
        }

        html, body {
            height: 100%;
        }

        body {
            font-family: var(--font-sans);
            background: var(--bg-primary);
            color: var(--text-primary);
            line-height: 1.5;
        }

        .login-page {
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 24px;
            background: radial-gradient(circle at top, var(--accent-light), transparent 55%), var(--bg-primary);
        }

        .login-card {
            width: min(420px, 100%);
            background: var(--bg-secondary);
            border: 1px solid var(--border);
            border-radius: var(--radius-lg);
            box-shadow: var(--shadow-md);
            padding: 32px;
            display: flex;
            flex-direction: column;
            gap: 24px;
        }

        .login-header {
            display: flex;
            flex-direction: column;
            gap: 12px;
            align-items: flex-start;
        }

        .logo {
            display: inline-flex;
            align-items: center;
            gap: 12px;
            font-weight: 600;
            font-size: 1.125rem;
        }

        .logo img {
            width: 36px;
            height: 36px;
            border-radius: var(--radius-sm);
        }

        .login-header p {
            color: var(--text-secondary);
            font-size: 0.95rem;
        }

        form {
            display: flex;
            flex-direction: column;
            gap: 16px;
        }

        label {
            display: flex;
            flex-direction: column;
            gap: 6px;
            font-size: 0.9rem;
            color: var(--text-secondary);
        }

        input {
            padding: 12px 14px;
            border-radius: var(--radius-md);
            border: 1px solid var(--border);
            background: var(--bg-secondary);
            color: var(--text-primary);
            font-size: 1rem;
            transition: border-color var(--transition-fast), box-shadow var(--transition-fast);
        }

        input:focus {
            outline: none;
            border-color: var(--accent);
            box-shadow: 0 0 0 3px var(--accent-light);
        }

        button {
            padding: 12px 16px;
            border-radius: var(--radius-md);
            border: none;
            background: var(--accent);
            color: white;
            font-weight: 600;
            font-size: 1rem;
            cursor: pointer;
            transition: background var(--transition-fast), transform var(--transition-fast);
        }

        button:hover {
            background: var(--accent-hover);
        }

        button:active {
            transform: translateY(1px);
        }

        .login-footer {
            font-size: 0.85rem;
            color: var(--text-tertiary);
        }
    </style>
</head>
<body>
    <div class="login-page">
        <div class="login-card">
            <div class="login-header">
                <div class="logo">
                    <img src="/logo.png" alt="Shrinkray logo">
                    <span>Shrinkray</span>
                </div>
                <h1>Sign in</h1>
                <p>Use your Shrinkray account to continue.</p>
            </div>
            <form method="POST" action="/auth/login">
                <label>
                    Username
                    <input name="username" autocomplete="username" required>
                </label>
                <label>
                    Password
                    <input type="password" name="password" autocomplete="current-password" required>
                </label>
                <button type="submit">Login</button>
            </form>
            <div class="login-footer">Protected access for Shrinkray administrators.</div>
        </div>
    </div>
</body>
</html>`))
		return err
	case http.MethodPost:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}

	username, password, err := readCredentials(r)
	if err != nil {
		return err
	}
	if ok, err := p.verifyPassword(username, password); err != nil || !ok {
		return errors.New("invalid credentials")
	}

	expires := time.Now().Add(p.sessionTTL)
	sessionValue := p.buildSessionValue(username, expires)
	http.SetCookie(w, &http.Cookie{
		Name:     p.cookieName,
		Value:    sessionValue,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		Secure:   r.TLS != nil,
	})

	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/", http.StatusFound)
		return nil
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (p *Provider) verifyPassword(username, password string) (bool, error) {
	hash, ok := p.users[username]
	if !ok {
		return false, nil
	}
	algo := p.hashAlgo
	if algo == "auto" {
		algo = detectHashAlgo(hash)
	}
	switch algo {
	case "bcrypt":
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
			return false, nil
		}
		return true, nil
	case "argon2", "argon2id", "argon2i":
		ok, err := verifyArgon2(password, hash)
		return ok, err
	default:
		return false, fmt.Errorf("unsupported hash algorithm: %s", algo)
	}
}

func detectHashAlgo(hash string) string {
	switch {
	case strings.HasPrefix(hash, "$2a$"),
		strings.HasPrefix(hash, "$2b$"),
		strings.HasPrefix(hash, "$2y$"):
		return "bcrypt"
	case strings.HasPrefix(hash, "$argon2id$"):
		return "argon2id"
	case strings.HasPrefix(hash, "$argon2i$"):
		return "argon2i"
	}
	return "bcrypt"
}

func (p *Provider) buildSessionValue(username string, expiry time.Time) string {
	payload := fmt.Sprintf("%s|%d", username, expiry.Unix())
	signature := p.sign(payload)
	return payload + "|" + signature
}

func (p *Provider) sign(payload string) string {
	mac := hmac.New(sha256.New, p.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (p *Provider) verifySession(value string) (string, time.Time, error) {
	parts := strings.Split(value, "|")
	if len(parts) != 3 {
		return "", time.Time{}, errors.New("invalid session format")
	}
	payload := parts[0] + "|" + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", time.Time{}, errors.New("invalid session signature")
	}
	expected := hmac.New(sha256.New, p.secret)
	expected.Write([]byte(payload))
	expectedSum := expected.Sum(nil)
	if subtle.ConstantTimeCompare(signature, expectedSum) != 1 {
		return "", time.Time{}, errors.New("invalid session signature")
	}
	expiryUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", time.Time{}, errors.New("invalid session expiry")
	}
	return parts[0], time.Unix(expiryUnix, 0), nil
}

func readCredentials(r *http.Request) (string, string, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		var payload struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return "", "", err
		}
		return strings.TrimSpace(payload.Username), payload.Password, nil
	}
	if err := r.ParseForm(); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(r.FormValue("username")), r.FormValue("password"), nil
}

type argon2Params struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
	keyLength   uint32
}

func verifyArgon2(password, encodedHash string) (bool, error) {
	variant, params, salt, hash, err := decodeArgon2Hash(encodedHash)
	if err != nil {
		return false, err
	}
	var derived []byte
	switch variant {
	case "argon2id":
		derived = argon2.IDKey([]byte(password), salt, params.iterations, params.memory, params.parallelism, params.keyLength)
	case "argon2i":
		derived = argon2.Key([]byte(password), salt, params.iterations, params.memory, params.parallelism, params.keyLength)
	default:
		return false, errors.New("unsupported argon2 variant")
	}
	if subtle.ConstantTimeCompare(hash, derived) != 1 {
		return false, nil
	}
	return true, nil
}

func decodeArgon2Hash(encodedHash string) (string, argon2Params, []byte, []byte, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) < 6 {
		return "", argon2Params{}, nil, nil, errors.New("invalid argon2 hash format")
	}
	if parts[1] != "argon2id" && parts[1] != "argon2i" {
		return "", argon2Params{}, nil, nil, errors.New("unsupported argon2 variant")
	}
	if !strings.HasPrefix(parts[2], "v=") {
		return "", argon2Params{}, nil, nil, errors.New("invalid argon2 version")
	}
	paramParts := strings.Split(parts[3], ",")
	params := argon2Params{}
	for _, part := range paramParts {
		keyVal := strings.SplitN(part, "=", 2)
		if len(keyVal) != 2 {
			return "", argon2Params{}, nil, nil, errors.New("invalid argon2 params")
		}
		value, err := strconv.ParseUint(keyVal[1], 10, 32)
		if err != nil {
			return "", argon2Params{}, nil, nil, errors.New("invalid argon2 params")
		}
		switch keyVal[0] {
		case "m":
			params.memory = uint32(value)
		case "t":
			params.iterations = uint32(value)
		case "p":
			params.parallelism = uint8(value)
		}
	}
	if params.memory == 0 || params.iterations == 0 || params.parallelism == 0 {
		return "", argon2Params{}, nil, nil, errors.New("invalid argon2 params")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return "", argon2Params{}, nil, nil, errors.New("invalid argon2 salt")
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return "", argon2Params{}, nil, nil, errors.New("invalid argon2 hash")
	}
	params.keyLength = uint32(len(hash))
	return parts[1], params, salt, hash, nil
}
