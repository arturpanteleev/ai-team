package cloudidentity

import (
	"fmt"
	"sort"
	"strings"
)

// StageAction — действие участника над этапом процесса. Право на действие
// определяется функцией этапа из закреплённого шаблона, а не глобальной
// матрицей permissions: один и тот же участник может брать этап разработчика,
// но не подтверждать его, если подтверждение назначено другой функции.
type StageAction string

const (
	// StageActionTake — взять этап, сменить исполнителя, запустить агента.
	StageActionTake StageAction = "take"
	// StageActionSubmit — сдать результат этапа (включая ответы grill).
	StageActionSubmit StageAction = "submit"
	// StageActionBypass — обойти проваленную проверку или пропустить этап.
	StageActionBypass StageAction = "bypass"
	// StageActionConfirm — подтвердить результат этапа или вернуть его назад.
	StageActionConfirm StageAction = "confirm"
)

// StageFunctions — функции этапа: исполнитель (`function`) и необязательный
// подтверждающий (`approver_function`). Пустой Approver означает, что
// подтверждает та же функция, что исполняет (поведение до B-82).
type StageFunctions struct {
	Executor string
	Approver string
}

// RequiredFunction возвращает функцию, которой разрешено действие. Take,
// submit и bypass принадлежат исполнителю (обходить проверку может
// ответственный за текущий этап); confirm — подтверждающему, а без него —
// исполнителю.
func (f StageFunctions) RequiredFunction(action StageAction) (string, error) {
	switch action {
	case StageActionTake, StageActionSubmit, StageActionBypass:
		return f.Executor, nil
	case StageActionConfirm:
		if strings.TrimSpace(f.Approver) != "" {
			return f.Approver, nil
		}
		return f.Executor, nil
	default:
		return "", fmt.Errorf("cloud RBAC: неизвестное действие этапа %q", action)
	}
}

// AuthorizeStageAction проверяет действие над этапом по функции этапа.
// Политика fail-closed: неизвестное действие, пустая или неизвестная функция
// этапа и отсутствие у участника назначенной функции запрещают действие.
// Возвращается каноническая роль, под которой действие разрешено.
func AuthorizeStageAction(principal Principal, action StageAction, functions StageFunctions) (Role, error) {
	function, err := functions.RequiredFunction(action)
	if err != nil {
		return "", err
	}
	role, ok := FunctionRole(function)
	if !ok {
		return "", fmt.Errorf("cloud RBAC: функция этапа %q не сопоставлена ни одной роли", function)
	}
	if !principal.Has(role) {
		return "", fmt.Errorf("cloud RBAC: actor %s не имеет функции %s для действия %s", principal.ActorID, role, action)
	}
	return role, nil
}

// ValidateStageFunctions проверяет пару «исполнитель / подтверждающий» из
// шаблона: подтверждающая функция необязательна, но если задана, то должна
// быть известной и отличаться от функции исполнителя с учётом синонимов
// (`po` и `product_owner` — одна функция).
func ValidateStageFunctions(functions StageFunctions) error {
	if functions.Approver == "" {
		return nil
	}
	if strings.TrimSpace(functions.Approver) != functions.Approver {
		return fmt.Errorf("approver_function не должен содержать пробелы по краям")
	}
	approver, ok := FunctionRole(functions.Approver)
	if !ok {
		return fmt.Errorf("approver_function %q не сопоставлен ни одной роли", functions.Approver)
	}
	if executor, ok := FunctionRole(functions.Executor); ok && executor == approver {
		return fmt.Errorf("approver_function %q совпадает с функцией исполнителя %q", functions.Approver, functions.Executor)
	}
	if strings.EqualFold(strings.TrimSpace(functions.Executor), functions.Approver) {
		return fmt.Errorf("approver_function %q совпадает с функцией исполнителя", functions.Approver)
	}
	return nil
}

// CheckSelfApproval запрещает подтверждение собственной работы, когда шаблон
// включает forbid_self_approval. Авторы — участники, бравшие этап или
// сдававшие его результат в текущем посещении.
func CheckSelfApproval(forbid bool, approverID string, authors []string) error {
	if !forbid {
		return nil
	}
	approverID = strings.TrimSpace(approverID)
	if approverID == "" {
		return fmt.Errorf("cloud RBAC: подтверждающий не определён")
	}
	for _, author := range authors {
		if strings.EqualFold(strings.TrimSpace(author), approverID) {
			return fmt.Errorf("cloud RBAC: actor %s не может подтверждать собственную работу", approverID)
		}
	}
	return nil
}

// ParseFunctions разбирает функции участника команды. Принимаются
// канонические роли и синонимы функций шаблона (bo, po, deployer); результат
// нормализован, отсортирован и без повторов.
func ParseFunctions(values []string) ([]Role, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("cloud identity: требуется хотя бы одна функция")
	}
	unique := make(map[Role]bool, len(values))
	for _, value := range values {
		role, ok := FunctionRole(value)
		if !ok {
			return nil, fmt.Errorf("cloud identity: неизвестная функция %q", value)
		}
		unique[role] = true
	}
	roles := make([]Role, 0, len(unique))
	for role := range unique {
		roles = append(roles, role)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i] < roles[j] })
	return roles, nil
}
