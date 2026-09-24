package web

import "time"

// maxBrowserSessions — верхняя граница числа одновременно живых browser-сессий.
// Дашборд обслуживает один локальный хост (CLI биндит сервер только на
// loopback), поэтому реальному пользователю с запасом хватает и десятка
// вкладок; запас до тысячи оставлен на перезагрузки страницы, которые каждый
// раз берут новую сессию. Граница нужна не ради «правильного» числа, а чтобы
// карта сессий вообще имела потолок: без неё каждый запрос к /api/session
// добавлял запись навсегда.
const maxBrowserSessions = 1024

// storeSession регистрирует выданную сессию, удерживая размер карты в
// пределах maxBrowserSessions.
//
// Порядок вытеснения: сначала истёкшие, потом — самая старая живая.
//
// Истёкшие записи раньше удалялись только в requestSession, то есть при
// обращении по их же cookie; сессия, о которой браузер забыл (закрытая
// вкладка, перезагрузка страницы), не удалялась никогда. Подметаем их на
// каждой выдаче: проход по карте ограничен maxBrowserSessions и случается
// только при создании сессии, то есть максимум раз на загрузку страницы.
//
// При достижении границы живыми сессиями выбран вариант «вытеснить самую
// старую», а не «отказать в новой». У отказа режим сбоя хуже: переполнить
// карту может и сам легитимный клиент (цикл перезагрузок), и тогда настоящий
// пользователь получает отказ во входе на все 8 часов TTL, пока записи не
// протухнут сами. При вытеснении деградация мягче — новый вход всегда
// проходит, ценой того, что самая старая сессия окажется разлогинена и
// клиенту придётся получить новую через /api/session. Уронить чужую сессию
// таким способом может лишь тот, кто уже имеет доступ к loopback-интерфейсу
// (а в cloud-режиме — ещё и валидный Bearer token, см. handleSession), то
// есть тот, кому и так доступно больше.
func (s *Server) storeSession(token string, session browserSession) {
	now := time.Now().UTC()
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	for key, existing := range s.sessions {
		if !existing.ExpiresAt.After(now) {
			delete(s.sessions, key)
		}
	}
	for len(s.sessions) >= maxBrowserSessions {
		s.evictOldestSessionLocked()
	}
	s.sessions[token] = session
}

// evictOldestSessionLocked удаляет сессию с самым ранним ExpiresAt. TTL у всех
// сессий одинаковый, поэтому самый ранний ExpiresAt — это и самая давно
// выданная сессия. Вызывается только под s.sessionMu и только при непустой
// карте, поэтому всегда удаляет ровно одну запись.
func (s *Server) evictOldestSessionLocked() {
	oldestKey := ""
	var oldestAt time.Time
	for key, existing := range s.sessions {
		if oldestKey == "" || existing.ExpiresAt.Before(oldestAt) {
			oldestKey, oldestAt = key, existing.ExpiresAt
		}
	}
	delete(s.sessions, oldestKey)
}
