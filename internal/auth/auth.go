// Package auth gère les mots de passe et les sessions du panel.
//
// Pas de dépendance externe volontairement (bcrypt n'est pas dans la stdlib) :
// on implémente un PBKDF2-HMAC-SHA256 maison, standard et éprouvé
// cryptographiquement, avec un nombre d'itérations élevé.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const pbkdf2Iterations = 200_000

func pbkdf2(password, salt []byte, iterations, keyLen int) []byte {
	numBlocks := (keyLen + sha256.Size - 1) / sha256.Size
	var out []byte
	mac := hmac.New(sha256.New, password)
	for block := 1; block <= numBlocks; block++ {
		mac.Reset()
		mac.Write(salt)
		be := []byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)}
		mac.Write(be)
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iterations; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// HashPassword génère un sel aléatoire et retourne (hashHex, saltHex).
func HashPassword(password string) (hashHex string, saltHex string, err error) {
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return "", "", err
	}
	hash := pbkdf2([]byte(password), salt, pbkdf2Iterations, 32)
	return hex.EncodeToString(hash), hex.EncodeToString(salt), nil
}

// VerifyPassword vérifie un mot de passe en temps constant.
func VerifyPassword(password, hashHex, saltHex string) bool {
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(hashHex)
	if err != nil {
		return false
	}
	got := pbkdf2([]byte(password), salt, pbkdf2Iterations, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ---- Sessions ----
//
// Un token de session est : base64(userID|expiryUnix) + "." + base64(hmac-sha256)
// Signé avec une clé secrète générée au premier démarrage et stockée sur disque
// (voir cmd principal). Pas d'état côté serveur à part cette clé : simple et léger.

var ErrInvalidToken = errors.New("session invalide ou expirée")

func CreateSessionToken(secret []byte, userID string, ttl time.Duration) string {
	expiry := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s|%d", userID, expiry)
	payloadB64 := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payloadB64))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payloadB64 + "." + sig
}

func ParseSessionToken(secret []byte, token string) (userID string, err error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", ErrInvalidToken
	}
	payloadB64, sig := parts[0], parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payloadB64))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expectedSig)) != 1 {
		return "", ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return "", ErrInvalidToken
	}
	fields := strings.SplitN(string(payload), "|", 2)
	if len(fields) != 2 {
		return "", ErrInvalidToken
	}
	expiry, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return "", ErrInvalidToken
	}
	if time.Now().Unix() > expiry {
		return "", ErrInvalidToken
	}
	return fields[0], nil
}

// NewSecret génère une clé secrète de 32 octets.
func NewSecret() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return b, err
}
