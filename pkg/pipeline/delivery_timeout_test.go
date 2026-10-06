package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
)

// hangingDeliveryService имитирует зависший `gh pr create`: команда не
// возвращается сама, единственный выход — отмена контекста. Ровно так ведёт
// себя настоящий контроллер, когда внешний бинарник ждёт сети или ввода.
type hangingDeliveryService struct {
	entered chan struct{}
	onEnter func()
}

func newHangingDeliveryService() *hangingDeliveryService {
	return &hangingDeliveryService{entered: make(chan struct{}, 1)}
}

func (h *hangingDeliveryService) Execute(ctx context.Context, _ delivery.Request) (delivery.Result, error) {
	select {
	case h.entered <- struct{}{}:
	default:
	}
	if h.onEnter != nil {
		h.onEnter()
	}
	<-ctx.Done()
	return delivery.Result{}, ctx.Err()
}

// deadlineProbeDeliveryService фиксирует дедлайн контекста, с которым пришла
// доставка, и состояние этого контекста на входе.
type deadlineProbeDeliveryService struct {
	hasDeadline bool
	remaining   time.Duration
	ctxErr      error
}

func (d *deadlineProbeDeliveryService) Execute(ctx context.Context, request delivery.Request) (delivery.Result, error) {
	deadline, ok := ctx.Deadline()
	d.hasDeadline = ok
	if ok {
		d.remaining = time.Until(deadline)
	}
	d.ctxErr = ctx.Err()
	hash, _ := request.Plan.Hash()
	return delivery.Result{PlanHash: hash, CommitSHA: strings.Repeat("c", 40), PRURL: "https://example.test/pr/9"}, nil
}

// deliveryRunCfg — конфиг двухстадийного run'а с delivery-стадией и явным
// delivery_timeout.
func deliveryRunCfg(timeout string) *config.Config {
	cfg := cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"})
	cfg.DeliveryTimeout = timeout
	return cfg
}

// QS-06. Post-terminal доставка обязана иметь СОБСТВЕННЫЙ дедлайн, а не
// наследовать budgetCtx run'а. Проверяется наблюдаемо: контекст, с которым
// доставка приходит в контроллер, имеет дедлайн порядка delivery_timeout
// (4 минуты), а не порядка max_wall_time (24 часа по умолчанию) и не «без
// дедлайна вовсе» — именно последнее давал context.Background().
func TestDeferredDeliveryGetsOwnDeadlineNotRunBudget(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	probe := &deadlineProbeDeliveryService{}
	p := New(deliveryRunCfg("4m"), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(probe))
	if err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
	}); err != nil {
		t.Fatalf("run с deferred delivery: %v", err)
	}
	if !probe.hasDeadline {
		t.Fatal("QS-06: доставка получила контекст без дедлайна (context.Background) — зависший git/gh не прервать")
	}
	if probe.ctxErr != nil {
		t.Fatalf("контекст доставки уже завершён на входе: %v", probe.ctxErr)
	}
	if probe.remaining <= 3*time.Minute || probe.remaining > 4*time.Minute {
		t.Fatalf("дедлайн доставки %v не похож на delivery_timeout=4m (бюджет run'а — 24h)", probe.remaining)
	}
}

// QS-06. Доставка отменяема сигналом: контекст процесса (signal.NotifyContext
// в cmd/ai-team) — родитель контекста доставки, поэтому Ctrl-C во время
// commit/push/PR прерывает её, а не оставляет контроллер висеть.
func TestDeferredDeliveryCancelledByProcessContext(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := newHangingDeliveryService()
	// «Ctrl-C» ровно в тот момент, когда доставка уже внутри внешней команды.
	service.onEnter = cancel

	p := New(deliveryRunCfg("30m"), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service))
	done := make(chan error, 1)
	go func() {
		done <- p.Run(ctx, RunConfig{
			Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
		})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("отменённая доставка обязана вернуть ошибку")
		}
		if !strings.Contains(err.Error(), "прервана извне") {
			t.Fatalf("ошибка не называет причину отмены: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("QS-06: доставка не прервалась отменой контекста процесса — контроллер висит на зависшей команде")
	}
}

// QS-06. Зависшая внешняя команда обрывается собственным дедлайном доставки,
// даже если сигнала не было, и ошибка называет причину. На коде до фикса
// (context.Background) доставка не возвращается никогда — тест падает по
// watchdog.
func TestDeferredDeliveryAbortsOnDeliveryTimeout(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	p := New(deliveryRunCfg("300ms"), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(newHangingDeliveryService()))
	done := make(chan error, 1)
	go func() {
		done <- p.Run(context.Background(), RunConfig{
			Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
		})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("доставка, упёршаяся в дедлайн, обязана вернуть ошибку")
		}
		if !strings.Contains(err.Error(), "delivery_timeout") {
			t.Fatalf("ошибка не называет delivery_timeout: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("QS-06: доставка не ограничена по времени — зависшая внешняя команда держит контроллер бесконечно")
	}
}

// QS-06. Тот же контракт на ручном пути (`ai-team deliver`): retry доставки
// тоже ограничен delivery_timeout.
func TestDeliverDeferredAbortsOnDeliveryTimeout(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	p := New(deliveryRunCfg("30m"), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(&gracefulDeliveryService{}))
	if err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
	}); err == nil {
		t.Fatal("post-terminal hook сбоит — Run обязан вернуть ошибку")
	}
	runDir := onlyRunDir(t, dir)

	retry := New(deliveryRunCfg("300ms"), nil, WithDeliveryService(newHangingDeliveryService()))
	done := make(chan error, 1)
	go func() {
		_, err := retry.DeliverDeferred(context.Background(), runDir, "", dir)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "delivery_timeout") {
			t.Fatalf("DeliverDeferred обязан упереться в delivery_timeout, got: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("QS-06: DeliverDeferred не ограничен по времени")
	}
}

// Дефолт delivery_timeout должен быть щедрым: доставка до живого remote
// измерена в 2.90 с, и бюджет не должен превращать медленную сеть в отказ.
func TestDefaultDeliveryTimeoutIsGenerous(t *testing.T) {
	if config.DefaultDeliveryTimeout < time.Minute {
		t.Fatalf("дефолт %v слишком тесный для push + gh по медленной сети", config.DefaultDeliveryTimeout)
	}
	var empty *config.Config
	if empty.EffectiveDeliveryTimeout() != config.DefaultDeliveryTimeout {
		t.Fatal("nil-конфиг обязан давать канонический дефолт, а не ноль")
	}
	cfg := &config.Config{DeliveryTimeout: "не длительность"}
	if cfg.EffectiveDeliveryTimeout() != config.DefaultDeliveryTimeout {
		t.Fatal("непарсящееся значение обязано давать канонический дефолт, а не ноль")
	}
	cfg.DeliveryTimeout = "0s"
	if cfg.EffectiveDeliveryTimeout() != config.DefaultDeliveryTimeout {
		t.Fatal("нулевой бюджет — это доставка без дедлайна, ровно дефект QS-06")
	}
	cfg.DeliveryTimeout = "90s"
	if cfg.EffectiveDeliveryTimeout() != 90*time.Second {
		t.Fatal("явный delivery_timeout обязан применяться")
	}
}
