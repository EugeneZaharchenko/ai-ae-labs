# E1 · Human-in-the-Loop

**Хто вирішує наступний крок:** людина · **Категорія:** E · людина й тригер

Робочий процес зупиняється в контрольній точці, показує людині структурований
контекст і продовжує з її відповіддю. Пауза — це подія в сесії, а не горутина,
що чекає на stdin. Тому вона переживає перезапуск процесу.

```
Start → prepare_payout → approve (паркується ⏸) ─ людина відповідає ─→ execute_payout
```

## Запуск

```bash
go run .              # сценарне демо: пауза → людина пише "approve" → виплата
go run . console      # людина — це ви: відповідайте approve або reject
go run . web api webui
```

Очікуваний слід офлайн-демо:

```
⚙️  prepare_payout ⇒ {"merchant":"A-114","amount":"1200 EUR"}
🙋 approve asks a human: Approve payout of 1200 EUR to A-114? (approve/reject)
👤 human answers adk_request_input: approve
⚙️  approve ⇒ {...,"decision":"approved"}
⚙️  execute_payout ⇒ payout of 1200 EUR to A-114 sent
```

## Go-примітиви (ADK Go v2.5.0)

| Примітив | Роль у демо |
|---|---|
| `workflow.ResumeOrRequestInput(ctx, emit, req)` | перший прохід — запит і пауза; після відновлення — відповідь людини |
| `session.RequestInput{InterruptID, Message, Payload}` | що саме бачить людина |
| `NodeConfig.RerunOnResume = &true` | після відповіді вузол `approve` виконується з початку |
| `workflow.NewEmittingFunctionNode` | вузол, який може емітити подію запиту |
| FunctionResponse `adk_request_input` | так клієнт (console, Web UI, `kit.Run`) повертає відповідь |

## Коли брати

Виплата коштів, лист клієнту, будь-яке рішення, яке має підписати названа
людина.

## Коли не брати

Повністю автономні потоки й часті дрібні рішення. Забагато гейтів — і люди
почнуть схвалювати не читаючи. Якщо ризик в одній дії, а не в плані, беріть
[E2](../e2_tool_confirmation_gate/).

## Що подивитися в коді

- **Побічний ефект — після відновлення.** `approve` виконується двічі (до і після
  паузи), тому він нічого не змінює. Гроші «йдуть» лише в `execute_payout`.
- **Без `RerunOnResume=&true`** рушій передає відповідь людини наступнику як
  вхід, а сам вузол не перезапускає. Тоді код після `ResumeOrRequestInput`
  не виконається.
- **`ResponseSchema` не примусова.** Рушій не перевіряє відповідь людини, тому
  `approve` сам зводить її до `approved`/`rejected`.
- `InterruptID` містить `InvocationID`: він стабільний у межах запуску і
  унікальний між запусками, тож Web UI не вважатиме новий запит уже відповіденим.
- Паралельний HITL у Go не підтримується: `workflow.ErrParallelHITLUnsupported`.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.5.0`
