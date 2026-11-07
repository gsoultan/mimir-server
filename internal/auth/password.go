// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is dual-licensed: you may use it under the GNU Affero General Public
// License, version 3, as published by the Free Software Foundation, or
// under a commercial license from Gembit Soultan Shirazi. See the LICENSE and
// LICENSING.md files at the root of this repository.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Affero
// General Public License for more details.

// Package auth decides who a caller is and what they may do.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters, per the OWASP 2024 recommendation.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024 // KiB
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
)

// MinPassword is the shortest password accepted; MaxPassword the
// longest, so a request can't make the server hash megabytes.
const (
	MinPassword = 10
	MaxPassword = 1024
)

// ErrWeakPassword reports a password shorter than MinPassword.
var ErrWeakPassword = fmt.Errorf("auth: password must be at least %d characters", MinPassword)

// ErrLongPassword reports a password longer than MaxPassword.
var ErrLongPassword = fmt.Errorf("auth: password must be at most %d characters", MaxPassword)

// HashPassword returns an encoded argon2id hash of password.
func HashPassword(password string) (string, error) {
	if len(password) < MinPassword {
		return "", ErrWeakPassword
	}
	if len(password) > MaxPassword {
		return "", ErrLongPassword
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches the encoded hash.
func CheckPassword(encoded, password string) bool {
	if len(password) > MaxPassword {
		return false // never stored, so never matches; and not worth hashing
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// KeyPrefix starts every API key, so a leaked one is easy to recognise.
const KeyPrefix = "mimir_"

// NewToken returns a random secret and the hash the store keeps for it.
// With prefix, the secret is an API key; without, a session token.
func NewToken(prefix string) (secret, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	secret = prefix + base64.RawURLEncoding.EncodeToString(b)
	return secret, HashToken(secret), nil
}

// HashToken returns the hash the store keeps for a secret.
func HashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ErrBadCredentials reports a sign-in that didn't match.
var ErrBadCredentials = errors.New("auth: wrong email or password")
