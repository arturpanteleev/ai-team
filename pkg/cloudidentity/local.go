package cloudidentity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// LocalOperatorActorID — actor ID человека, запустившего `ai-team web` на
// своей машине. Отличается от CLI-шного "local-user" намеренно: решение,
// записанное через браузер, должно быть отличимо в evidence от решения,
// записанного в терминале, хотя полномочия у них одинаковые.
const LocalOperatorActorID = "local-operator"

// minLocalTokenBytes — нижняя граница длины предоставленного извне local
// token. Сгенерированный token всегда 32 случайных байта; заданный через
// окружение не может быть короче 16 символов, иначе его можно перебрать по
// loopback быстрее, чем оператор заметит.
const minLocalTokenBytes = 16

// LocalOperator — IdentityVerifier для loopback-режима без cloud credential.
//
// Локальный режим — основной сценарий продукта, и до QS-02 он работал вообще
// без identity: любой процесс, доставший loopback-порт, объявлял свою роль в
// теле запроса и утверждал delivery plan. Вместо того чтобы закрыть write API
// совсем (это сломало бы заявленный в README путь «принять approval в UI»),
// процесс `ai-team web` выпускает один случайный token на время своей жизни и
// печатает его в консоль. Token проверяется тем же механизмом, что cloud
// Bearer, и так же обменивается на HttpOnly browser-session.
//
// Principal локального оператора держит все канонические роли: его
// полномочия равны полномочиям того, кто и так может запустить CLI в этом
// репозитории. Гейтом является владение token, а не набор ролей.
type LocalOperator struct {
	token     string
	principal Principal
}

// NewLocalOperator генерирует случайный token и возвращает verifier вместе с
// ним. Token нигде не сохраняется: он живёт столько же, сколько процесс.
func NewLocalOperator() (*LocalOperator, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("local operator token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	operator, err := NewLocalOperatorWithToken(token)
	if err != nil {
		return nil, "", err
	}
	return operator, token, nil
}

// NewLocalOperatorWithToken строит verifier поверх заданного token. Нужен для
// автоматизации (тесты, скрипты), которая обязана знать token заранее.
func NewLocalOperatorWithToken(token string) (*LocalOperator, error) {
	token = strings.TrimSpace(token)
	if len(token) < minLocalTokenBytes {
		return nil, fmt.Errorf("local operator token: требуется не менее %d символов", minLocalTokenBytes)
	}
	principal, err := NewPrincipal(LocalOperatorActorID, AllRoles())
	if err != nil {
		return nil, err
	}
	return &LocalOperator{token: token, principal: principal}, nil
}

// Verify сравнивает предъявленный token с выданным за постоянное время.
func (l *LocalOperator) Verify(token string) (Principal, error) {
	presented := strings.TrimSpace(token)
	if len(presented) != len(l.token) ||
		subtle.ConstantTimeCompare([]byte(presented), []byte(l.token)) != 1 {
		return Principal{}, errors.New("local operator token отклонён")
	}
	return l.principal, nil
}

// AllRoles возвращает канонические роли в детерминированном порядке.
func AllRoles() []Role {
	return []Role{
		RoleProductOwner, RoleArchitect, RoleDeveloper,
		RoleReviewer, RoleQA, RoleReleaseManager,
	}
}
