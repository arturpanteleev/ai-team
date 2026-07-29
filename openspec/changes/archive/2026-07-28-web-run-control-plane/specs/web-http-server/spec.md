## ADDED Requirements

### Requirement: Session bootstrap
HTTP server MUST генерировать независимые случайные session и CSRF tokens для
каждого process start.

#### Scenario: Same-origin bootstrap
- **КОГДА** browser вызывает `GET /api/session` с допустимым Host/Origin
- **ТОГДА** server MUST установить HttpOnly SameSite=Strict session-cookie
- **И** MUST вернуть CSRF token в JSON без permissive CORS

### Requirement: Bounded command bodies
Write handlers MUST ограничивать body и строго декодировать единственный JSON
document.

#### Scenario: Oversized body
- **КОГДА** command body превышает установленный limit
- **ТОГДА** server MUST вернуть 400 и не вызывать controller
